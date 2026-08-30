// The `response` object handed to a post-response script. Built from a JSON
// envelope + the raw body string that Go injects, rather than from a wrapped
// Go struct, so every value a script touches is a plain JS value with plain
// JS semantics (deep-equal, JSON.stringify, Object.keys all behave).
(function (global) {
  'use strict';

  var auk = global.__auk;
  var meta = JSON.parse(global.__responseJSON || '{}');
  var body = global.__responseBody === undefined || global.__responseBody === null
    ? ''
    : String(global.__responseBody);

  delete global.__responseJSON;
  delete global.__responseBody;

  var pairs = meta.headers || [];

  // headers is a plain object keyed by the header name as the server sent it
  // (first wins on duplicates, matching the rest of the app's header
  // lookups), so Object.keys(response.headers) is clean. Case-insensitive
  // .get()/.getAll() are defined non-enumerable so they don't show up in
  // that iteration.
  var headers = {};
  var i;
  for (i = 0; i < pairs.length; i++) {
    var k = String(pairs[i][0]);
    if (!Object.prototype.hasOwnProperty.call(headers, k)) headers[k] = String(pairs[i][1]);
  }

  function defineHidden(obj, name, value) {
    Object.defineProperty(obj, name, { value: value, enumerable: false, writable: true, configurable: true });
  }

  defineHidden(headers, 'get', function (name) {
    var want = String(name).toLowerCase();
    for (var j = 0; j < pairs.length; j++) {
      if (String(pairs[j][0]).toLowerCase() === want) return String(pairs[j][1]);
    }
    return undefined;
  });

  defineHidden(headers, 'getAll', function (name) {
    var want = String(name).toLowerCase();
    var out = [];
    for (var j = 0; j < pairs.length; j++) {
      if (String(pairs[j][0]).toLowerCase() === want) out.push(String(pairs[j][1]));
    }
    return out;
  });

  var parsed;
  var parsedOK = false;

  var response = {
    status: meta.status || 0,
    statusText: meta.statusText === undefined || meta.statusText === null ? '' : String(meta.statusText),
    headers: headers,
    body: body,
    timingMs: meta.timingMs || 0,
    size: meta.size || 0,
    // json() parses lazily and caches, and fails with a message that says
    // what the body actually was — "unexpected token <" with no context is
    // the worst thirty seconds of debugging an API client can give you.
    json: function () {
      if (parsedOK) return parsed;
      try {
        parsed = JSON.parse(body);
      } catch (e) {
        throw new Error(
          'response.json(): the response body is not valid JSON — ' + auk.errMessage(e) +
          (body.length === 0 ? ' (the body is empty)' : ' (body starts: ' + auk.truncate(body, 120) + ')')
        );
      }
      parsedOK = true;
      return parsed;
    }
  };

  // A script reports on a response; it never edits the one that gets stored.
  // Go simply doesn't read this object back, but freezing makes that visible
  // inside the script too.
  Object.freeze(response);
  global.response = response;
})(this);
