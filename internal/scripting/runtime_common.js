// Shared script runtime, installed into EVERY AUK script VM (pre-request and
// post-response) before the user's script runs.
//
// Deliberately conservative ES5: no arrow functions, classes, or template
// literals. This has to behave identically on any sobek build, and a sandbox
// boundary is easier to audit when there is no syntax in the way.
//
// Go injects the raw inputs as `__`-prefixed globals; this file captures them
// into closure variables and then DELETES them, so a user script cannot call
// the low-level sinks directly to sidestep the checks below (notably the
// secrets guard in vars.set). The engine re-applies that guard when it
// persists, so this is belt and braces rather than the only lock.
(function (global) {
  'use strict';

  var MAX_LOG_LINE = 10000;

  var recordLog = global.__log;
  var recordVarSet = global.__varSet;
  var recordVarUnset = global.__varUnset;
  var initialVars = JSON.parse(global.__initialVarsJSON || '{}');
  var secretNames = JSON.parse(global.__secretNamesJSON || '[]');

  delete global.__log;
  delete global.__varSet;
  delete global.__varUnset;
  delete global.__initialVarsJSON;
  delete global.__secretNamesJSON;

  function isArray(v) {
    return Object.prototype.toString.call(v) === '[object Array]';
  }

  function truncate(s, n) {
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // fmt renders a value for an assertion message: strings quoted (so a
  // trailing space or an empty string is visible), everything else in its
  // most recognisable JSON-ish form. Message quality is the whole point of
  // the expect() surface, so this is used everywhere a value is reported.
  function fmt(v) {
    if (typeof v === 'string') return JSON.stringify(v);
    if (v === undefined) return 'undefined';
    if (v === null) return 'null';
    if (typeof v === 'number' || typeof v === 'boolean') return String(v);
    if (typeof v === 'function') return 'function';
    if (v instanceof RegExp) return String(v);
    try {
      var s = JSON.stringify(v);
      return s === undefined ? String(v) : truncate(s, 400);
    } catch (e) {
      return String(v);
    }
  }

  function deepEqual(a, b) {
    if (a === b) return true;
    // NaN === NaN is false, but toEqual(NaN) should pass.
    if (typeof a === 'number' && typeof b === 'number') return a !== a && b !== b;
    if (a === null || b === null) return false;
    if (typeof a !== 'object' || typeof b !== 'object') return false;
    var aArr = isArray(a);
    if (aArr !== isArray(b)) return false;
    if (aArr) {
      if (a.length !== b.length) return false;
      for (var i = 0; i < a.length; i++) {
        if (!deepEqual(a[i], b[i])) return false;
      }
      return true;
    }
    var ka = Object.keys(a);
    var kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    for (var j = 0; j < ka.length; j++) {
      if (!Object.prototype.hasOwnProperty.call(b, ka[j])) return false;
      if (!deepEqual(a[ka[j]], b[ka[j]])) return false;
    }
    return true;
  }

  // pathParts splits "data.items[0].id" into ["data","items","0","id"] so
  // toHaveProperty takes the same shape of path the assertion editor and
  // json.get() already use.
  function pathParts(path) {
    var out = [];
    var cur = '';
    for (var i = 0; i < path.length; i++) {
      var ch = path.charAt(i);
      if (ch === '.' || ch === '[') {
        if (cur !== '') { out.push(cur); cur = ''; }
      } else if (ch === ']') {
        if (cur !== '') { out.push(unquote(cur)); cur = ''; }
      } else {
        cur += ch;
      }
    }
    if (cur !== '') out.push(cur);
    return out;
  }

  function unquote(s) {
    if (s.length >= 2) {
      var first = s.charAt(0);
      if ((first === '"' || first === "'") && s.charAt(s.length - 1) === first) {
        return s.slice(1, -1);
      }
    }
    return s;
  }

  // getPath walks a dotted path, reporting whether it existed at all
  // separately from its value (so toHaveProperty distinguishes "absent" from
  // "present but undefined").
  function getPath(obj, path) {
    var parts = pathParts(path);
    var cur = obj;
    for (var i = 0; i < parts.length; i++) {
      if (cur === null || cur === undefined || typeof cur !== 'object') {
        return { found: false, value: undefined };
      }
      if (!Object.prototype.hasOwnProperty.call(cur, parts[i])) {
        return { found: false, value: undefined };
      }
      cur = cur[parts[i]];
    }
    return { found: true, value: cur };
  }

  function errMessage(e) {
    if (e === null || e === undefined) return String(e);
    if (typeof e === 'object' && e.message !== undefined && e.message !== null) {
      return String(e.message);
    }
    return String(e);
  }

  // __auk is the internal helper namespace the other runtime files use. Not
  // part of the documented script API.
  global.__auk = {
    fmt: fmt,
    deepEqual: deepEqual,
    getPath: getPath,
    errMessage: errMessage,
    isArray: isArray,
    truncate: truncate
  };

  // ---- console -----------------------------------------------------------
  // Captured into a Go-side slice, never written to the process stdout: a
  // script running inside the GUI, the CLI, or the MCP server must not be
  // able to scribble on their output streams.
  function formatLine(args) {
    var parts = [];
    for (var i = 0; i < args.length; i++) {
      parts.push(typeof args[i] === 'string' ? args[i] : fmt(args[i]));
    }
    var line = parts.join(' ');
    return line.length > MAX_LOG_LINE ? line.slice(0, MAX_LOG_LINE) + '… (truncated)' : line;
  }

  var consoleObj = {
    log: function () { recordLog(formatLine(arguments)); }
  };
  // info/warn/error/debug are aliases: one capture stream, no levels.
  consoleObj.info = consoleObj.log;
  consoleObj.warn = consoleObj.log;
  consoleObj.error = consoleObj.log;
  consoleObj.debug = consoleObj.log;
  global.console = consoleObj;

  // ---- vars --------------------------------------------------------------
  var secretSet = {};
  for (var s = 0; s < secretNames.length; s++) secretSet[String(secretNames[s])] = true;

  var store = {};
  var keys = Object.keys(initialVars);
  for (var k = 0; k < keys.length; k++) store[keys[k]] = String(initialVars[keys[k]]);

  function requireName(fn, name) {
    if (typeof name !== 'string' || name === '') {
      throw new Error(fn + ': name must be a non-empty string, got ' + fmt(name));
    }
    return name;
  }

  function refuseSecret(fn, name) {
    if (secretSet[name]) {
      throw new Error(
        fn + '("' + name + '"): "' + name + '" is a secret in this environment. Its value lives in the OS ' +
        'keychain and is never written to disk, so a script may not overwrite or delete it.'
      );
    }
  }

  // coerce keeps the variable set a plain string -> string map (that is what
  // ${...} templating consumes). undefined/null THROW rather than storing an
  // empty string: `vars.set('token', body.token)` against a response whose
  // shape changed is the single most common chaining bug, and silently
  // storing "" turns it into a mystery 401 two requests later.
  function coerce(fn, name, value) {
    if (value === undefined || value === null) {
      throw new Error(
        fn + '("' + name + '"): value is ' + (value === null ? 'null' : 'undefined') + ', nothing was stored. ' +
        'A path that did not match the response is the usual cause; use vars.unset("' + name + '") to clear it.'
      );
    }
    if (typeof value === 'string') return value;
    if (typeof value === 'number' || typeof value === 'boolean') return String(value);
    try {
      var json = JSON.stringify(value);
      if (typeof json === 'string') return json;
    } catch (e) { /* fall through to String() */ }
    return String(value);
  }

  global.vars = {
    // get returns undefined for an unset name (not ''), so
    // expect(vars.get('token')).toBeTruthy() reads the way you would expect.
    get: function (name) {
      var n = requireName('vars.get', name);
      return Object.prototype.hasOwnProperty.call(store, n) ? store[n] : undefined;
    },
    set: function (name, value) {
      var n = requireName('vars.set', name);
      refuseSecret('vars.set', n);
      var v = coerce('vars.set', n, value);
      store[n] = v;
      recordVarSet(n, v);
      return v;
    },
    unset: function (name) {
      var n = requireName('vars.unset', name);
      refuseSecret('vars.unset', n);
      delete store[n];
      recordVarUnset(n);
    }
  };
})(this);
