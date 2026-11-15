import test from 'node:test';
import assert from 'node:assert/strict';
import {clampPosition} from './web/seek.mjs';

test('clampPosition keeps values inside the track', () => {
  assert.equal(clampPosition(-1, 100000), 0);
  assert.equal(clampPosition(50000, 100000), 50000);
  assert.equal(clampPosition(120000, 100000), 99000);
  assert.equal(clampPosition(1, 100000), 1);
});

test('clampPosition handles unknown duration and bad input', () => {
  assert.equal(clampPosition(50000, 0), 50000);
  assert.equal(clampPosition(NaN, 100000), 0);
  assert.equal(clampPosition(999, 500), 0);
});

test('clampPosition respects the 1s end margin', () => {
  assert.equal(clampPosition(99500, 100000), 99000);
  assert.equal(clampPosition(99000, 100000), 99000);
  assert.equal(clampPosition(Infinity, 100000), 0);
});
