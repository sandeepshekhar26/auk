// test() and expect() — the assertion surface of a post-response script.
//
// Every failure message is written to be read on its own, out of context, in
// a CI log or a JUnit report: "expected 404 to be 200", not "assertion
// failed". That message becomes TestResult.Error verbatim, so message
// quality IS the feature here.
(function (global) {
  'use strict';

  var auk = global.__auk;
  var fmt = auk.fmt;
  var recordTest = global.__recordTest;
  delete global.__recordTest;
  // Captured then removed from the global so a script can't reach the Go sink.
  var reMatch = global.__reMatch;
  delete global.__reMatch;

  function typeName(v) {
    if (v === null) return 'null';
    if (auk.isArray(v)) return 'array';
    return typeof v;
  }

  // usageError is thrown for a matcher used against the wrong kind of value
  // (expect(3).toContain('a')). It is a bug in the test, not a failed
  // assertion, so .not never flips it.
  function usageError(matcher, message) {
    return new Error('expect(...).' + matcher + ' ' + message);
  }

  function makeExpect(actual, negated) {
    // check() takes the POSITIVE outcome plus both messages and applies the
    // negation once, so every matcher below reads as its plain positive form.
    function check(pass, message, negatedMessage) {
      if (negated ? pass : !pass) {
        throw new Error(negated ? negatedMessage : message);
      }
    }

    var api = {
      toBe: function (expected) {
        check(
          actual === expected,
          'expected ' + fmt(actual) + ' to be ' + fmt(expected),
          'expected ' + fmt(actual) + ' not to be ' + fmt(expected)
        );
        return api;
      },

      toEqual: function (expected) {
        check(
          auk.deepEqual(actual, expected),
          'expected ' + fmt(actual) + ' to equal ' + fmt(expected),
          'expected ' + fmt(actual) + ' not to equal ' + fmt(expected)
        );
        return api;
      },

      toBeTruthy: function () {
        check(
          !!actual,
          'expected ' + fmt(actual) + ' to be truthy',
          'expected ' + fmt(actual) + ' not to be truthy'
        );
        return api;
      },

      toBeFalsy: function () {
        check(
          !actual,
          'expected ' + fmt(actual) + ' to be falsy',
          'expected ' + fmt(actual) + ' not to be falsy'
        );
        return api;
      },

      toContain: function (expected) {
        var pass;
        if (typeof actual === 'string') {
          pass = actual.indexOf(String(expected)) !== -1;
        } else if (auk.isArray(actual)) {
          pass = false;
          for (var i = 0; i < actual.length; i++) {
            if (auk.deepEqual(actual[i], expected)) { pass = true; break; }
          }
        } else {
          throw usageError('toContain', 'needs a string or an array, got ' + typeName(actual));
        }
        check(
          pass,
          'expected ' + fmt(actual) + ' to contain ' + fmt(expected),
          'expected ' + fmt(actual) + ' not to contain ' + fmt(expected)
        );
        return api;
      },

      toBeGreaterThan: function (expected) {
        if (typeof actual !== 'number' || typeof expected !== 'number') {
          throw usageError('toBeGreaterThan', 'needs numbers, got ' + typeName(actual) + ' and ' + typeName(expected));
        }
        check(
          actual > expected,
          'expected ' + fmt(actual) + ' to be greater than ' + fmt(expected),
          'expected ' + fmt(actual) + ' not to be greater than ' + fmt(expected)
        );
        return api;
      },

      toBeLessThan: function (expected) {
        if (typeof actual !== 'number' || typeof expected !== 'number') {
          throw usageError('toBeLessThan', 'needs numbers, got ' + typeName(actual) + ' and ' + typeName(expected));
        }
        check(
          actual < expected,
          'expected ' + fmt(actual) + ' to be less than ' + fmt(expected),
          'expected ' + fmt(actual) + ' not to be less than ' + fmt(expected)
        );
        return api;
      },

      // toMatch treats its argument as a regular-expression SOURCE (not a
      // substring — that is what toContain is for). The match runs through
      // Go's RE2 engine (linear time) rather than the JS regex engine, whose
      // backtracking fallback can hang on a catastrophic pattern; a pattern
      // RE2 cannot compile (true lookahead/backreferences) is reported as a
      // usage error instead of silently running on the unsafe engine.
      toMatch: function (expected) {
        if (typeof actual !== 'string') {
          throw usageError('toMatch', 'needs a string, got ' + typeName(actual));
        }
        // Flags must survive the hop to Go's RE2: taking only `.source` made
        // `expect(body).toMatch(/error/i)` case-SENSITIVE, so the negated form
        // `not.toMatch(/error/i)` reported a false GREEN on a body containing
        // "ERROR" — exactly the case-insensitive error sniffing the flag
        // exists for. RE2 has no flags argument, so the supported flags are
        // translated into an inline group: i (case-insensitive), m (multiline
        // ^/$), s (dotall, JS's `dotAll`). g/y are irrelevant to a boolean
        // match and u is RE2's default, so all three are ignored silently.
        var source = expected instanceof RegExp ? expected.source : String(expected);
        var jsFlags = expected instanceof RegExp ? String(expected.flags || '') : '';
        var re2Flags = '';
        if (jsFlags.indexOf('i') >= 0) re2Flags += 'i';
        if (jsFlags.indexOf('m') >= 0) re2Flags += 'm';
        if (jsFlags.indexOf('s') >= 0) re2Flags += 's';
        if (re2Flags) source = '(?' + re2Flags + ')' + source;
        var shown = '/' + (expected instanceof RegExp ? expected.source : source) + '/' + jsFlags;
        var r = reMatch(source, actual);
        if (r.charAt(0) === '!') {
          throw usageError('toMatch', 'was given a regular expression ' + shown + ' that AUK cannot run safely: ' + r.slice(1));
        }
        check(
          r === '1',
          'expected ' + fmt(actual) + ' to match ' + shown,
          'expected ' + fmt(actual) + ' not to match ' + shown
        );
        return api;
      },

      // toHaveProperty(path) checks a dotted/bracketed path exists;
      // toHaveProperty(path, value) also deep-equals the value there.
      toHaveProperty: function (path, expectedValue) {
        if (actual === null || actual === undefined || typeof actual !== 'object') {
          throw usageError('toHaveProperty', 'needs an object, got ' + typeName(actual));
        }
        if (typeof path !== 'string' || path === '') {
          throw usageError('toHaveProperty', 'needs a non-empty path string, got ' + fmt(path));
        }
        var hit = auk.getPath(actual, path);
        if (arguments.length < 2) {
          check(
            hit.found,
            'expected ' + fmt(actual) + ' to have property ' + fmt(path),
            'expected ' + fmt(actual) + ' not to have property ' + fmt(path)
          );
          return api;
        }
        check(
          hit.found && auk.deepEqual(hit.value, expectedValue),
          'expected property ' + fmt(path) + ' to equal ' + fmt(expectedValue) +
            (hit.found ? ', got ' + fmt(hit.value) : ', but it is not present'),
          'expected property ' + fmt(path) + ' not to equal ' + fmt(expectedValue)
        );
        return api;
      }
    };

    // .not carries the same matchers with the outcome flipped. Built once,
    // and only on the positive side, so it terminates.
    if (!negated) api.not = makeExpect(actual, true);
    return api;
  }

  global.expect = function (actual) {
    return makeExpect(actual, false);
  };

  // test() catches PER TEST: one failing check must never stop the rest of
  // the suite from running and reporting.
  global.test = function (name, fn) {
    var label = name === undefined || name === null ? '' : String(name);
    if (typeof fn !== 'function') {
      recordTest(label, false, 'test(' + fmt(label) + '): the second argument must be a function, got ' + typeName(fn));
      return;
    }
    try {
      var result = fn();
      // An async test callback returns a Promise whose rejection this
      // synchronous runner can never observe — so an async test that FAILS
      // would be recorded as PASSED (a false green, the worst outcome for a
      // test tool). There is no event loop to await it on, so reject async
      // callbacks outright rather than silently passing them.
      if (result && typeof result.then === 'function') {
        // Attach a no-op rejection handler so this promise doesn't ALSO
        // surface as an unhandled rejection (which would fail the whole run
        // with a second, confusing error) — we've already recorded the async
        // test as a failure right here, which is the clear outcome.
        if (typeof result.catch === 'function') {
          result.catch(function () {});
        }
        recordTest(
          label,
          false,
          'test(' + fmt(label) + '): the callback returned a Promise — async tests are not supported. ' +
            'Make your assertions synchronously; response.json()/body are already available without awaiting.',
        );
        return;
      }
      recordTest(label, true, '');
    } catch (e) {
      recordTest(label, false, auk.errMessage(e));
    }
  };
})(this);
