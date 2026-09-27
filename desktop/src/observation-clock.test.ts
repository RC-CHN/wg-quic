import assert from 'node:assert/strict';
import { ObservationClock } from './observation-clock';

const clock = new ObservationClock();
const beforeStop = clock.capture();
clock.invalidate();
assert.equal(clock.accepts(beforeStop), false, 'late status must not undo a completed stop');
assert.equal(clock.accepts(clock.capture()), true, 'new observations can update the result');
