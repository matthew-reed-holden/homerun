#!/bin/sh
# Stub act for tests: records its args and env, replays a log, exits as told.
[ -n "$FAKE_ACT_ARGS" ] && printf '%s\n' "$@" > "$FAKE_ACT_ARGS"
[ -n "$FAKE_ACT_ENV" ] && env > "$FAKE_ACT_ENV"
[ -n "$FAKE_ACT_LS" ] && ls -A > "$FAKE_ACT_LS"
[ -n "$FAKE_ACT_OUTPUT" ] && cat "$FAKE_ACT_OUTPUT"
echo "fake act stderr" >&2
[ -n "$FAKE_ACT_SLEEP" ] && exec sleep "$FAKE_ACT_SLEEP"
exit "${FAKE_ACT_EXIT:-0}"
