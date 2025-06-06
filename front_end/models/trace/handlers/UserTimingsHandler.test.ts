// Copyright 2022 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

import {describeWithEnvironment} from '../../../testing/EnvironmentHelpers.js';
import {
  makeCompleteEvent,
} from '../../../testing/TraceHelpers.js';
import {TraceLoader} from '../../../testing/TraceLoader.js';
import * as Trace from '../trace.js';

describeWithEnvironment('UserTimingsHandler', function() {
  let timingsData: Trace.Handlers.ModelHandlers.UserTimings.UserTimingsData;
  describe('performance timings', function() {
    async function getTimingsDataFromEvents(events: readonly Trace.Types.Events.Event[]):
        Promise<Trace.Handlers.ModelHandlers.UserTimings.UserTimingsData> {
      Trace.Handlers.ModelHandlers.UserTimings.reset();
      for (const event of events) {
        Trace.Handlers.ModelHandlers.UserTimings.handleEvent(event);
      }
      await Trace.Handlers.ModelHandlers.UserTimings.finalize();
      return Trace.Handlers.ModelHandlers.UserTimings.data();
    }
    beforeEach(async function() {
      const events = await TraceLoader.rawEvents(this, 'user-timings.json.gz');
      timingsData = await getTimingsDataFromEvents(events);
    });
    afterEach(function() {
      Trace.Handlers.ModelHandlers.UserTimings.reset();
    });
    describe('performance.measure events parsing', function() {
      it('parses the start and end events and returns a list of blocks', async () => {
        assert.lengthOf(timingsData.performanceMeasures, 3);
        assert.strictEqual(
            Trace.Helpers.Trace.extractId(timingsData.performanceMeasures[1]),
            'blink.user_timing:0x9072211:first measure');
        assert.strictEqual(timingsData.performanceMeasures[1].name, 'first measure');
        assert.strictEqual(
            Trace.Helpers.Trace.extractId(timingsData.performanceMeasures[0]),
            'blink.user_timing:0x6ece31c8:second measure');
        assert.strictEqual(timingsData.performanceMeasures[0].name, 'second measure');
        assert.strictEqual(
            Trace.Helpers.Trace.extractId(timingsData.performanceMeasures[2]),
            'blink.user_timing:0x10c31982:third measure');
        assert.strictEqual(timingsData.performanceMeasures[2].name, 'third measure');

        // Ensure we assign begin + end the right way round by making sure the
        // beginEvent is the ASYNC_NESTABLE_START and the endEvent is the
        // ASYNC_NESTABLE_END.
        for (let i = 0; i < timingsData.performanceMeasures.length; i++) {
          assert.strictEqual(
              timingsData.performanceMeasures[i].args.data.beginEvent.ph,
              Trace.Types.Events.Phase.ASYNC_NESTABLE_START);
          assert.strictEqual(
              timingsData.performanceMeasures[i].args.data.endEvent.ph, Trace.Types.Events.Phase.ASYNC_NESTABLE_END);
        }
      });

      it('sorts the blocks to ensure they are in time order', async function() {
        const events = await TraceLoader.rawEvents(this, 'user-timings.json.gz');
        Trace.Handlers.ModelHandlers.UserTimings.reset();
        // Reverse the array so that the events are in the wrong order.
        // This _shouldn't_ ever happen in a real trace, but it's best for us to
        // sort the blocks once we've parsed them just in case.
        const reversed = events.slice().reverse();
        for (const event of reversed) {
          Trace.Handlers.ModelHandlers.UserTimings.handleEvent(event);
        }
        await Trace.Handlers.ModelHandlers.UserTimings.finalize();
        const data = Trace.Handlers.ModelHandlers.UserTimings.data();
        assert.lengthOf(data.performanceMeasures, 3);
        assert.isTrue(data.performanceMeasures[0].ts <= data.performanceMeasures[1].ts);
        assert.isTrue(data.performanceMeasures[1].ts <= data.performanceMeasures[2].ts);
      });

      it('calculates the duration correctly from the begin/end event timestamps', async function() {
        for (const timing of timingsData.performanceMeasures) {
          // Ensure for each timing pair we've set the dur correctly.
          assert.strictEqual(timing.dur, timing.args.data.endEvent.ts - timing.args.data.beginEvent.ts);
        }
      });
      it('correctly extracts nested timings in the correct order', async function() {
        const {parsedTrace} = await TraceLoader.traceEngine(this, 'user-timings-complex.json.gz');
        const complexTimingsData = parsedTrace.UserTimings;
        const userTimingEventNames = [];
        for (const event of complexTimingsData.performanceMeasures) {
          // This trace has multiple user timings events, in this instance we only care about the ones that include 'nested' in the name.
          if (event.name.includes('nested')) {
            userTimingEventNames.push(event.name);
          }
        }
        assert.deepEqual(userTimingEventNames, [
          'nested-a',
          'nested-b',
          'nested-c',
          'nested-d',
        ]);
      });
      it('correctly orders measures when one measure encapsulates the others', async function() {
        const events = await TraceLoader.rawEvents(this, 'user-timings-complex.json.gz');
        const complexTimingsData = await getTimingsDataFromEvents(events);
        const userTimingEventNames = [];
        for (const event of complexTimingsData.performanceMeasures) {
          // This trace has multiple user timings events, in this instance we only care about the ones that start with 'duration'
          if (event.name.startsWith('duration')) {
            userTimingEventNames.push(event.name);
          }
        }
        assert.deepEqual(userTimingEventNames, [
          'durationTimeTotal',
          'durationTime1',
          'durationTime2',
        ]);
      });
    });
    describe('performance.mark events parsing', function() {
      it('parses performance mark events correctly', function() {
        assert.lengthOf(timingsData.performanceMarks, 2);
        assert.strictEqual(timingsData.performanceMarks[0].name, 'mark1');
        assert.strictEqual(timingsData.performanceMarks[1].name, 'mark3');
      });
    });
  });
  describe('console timings', function() {
    beforeEach(async function() {
      const {parsedTrace} = await TraceLoader.traceEngine(this, 'timings-track.json.gz');
      timingsData = parsedTrace.UserTimings;
    });
    describe('console.time events parsing', function() {
      it('parses the start and end events and returns a list of blocks', async () => {
        assert.lengthOf(timingsData.consoleTimings, 3);
        assert.strictEqual(
            Trace.Helpers.Trace.extractId(timingsData.consoleTimings[0]),
            'blink.console:0x12c00282160:first console time');
        assert.strictEqual(timingsData.consoleTimings[0].name, 'first console time');
        assert.strictEqual(
            Trace.Helpers.Trace.extractId(timingsData.consoleTimings[1]),
            'blink.console:0x12c00282160:second console time');
        assert.strictEqual(timingsData.consoleTimings[1].name, 'second console time');

        // Ensure we assign begin + end the right way round by making sure the
        // beginEvent is the ASYNC_NESTABLE_START and the endEvent is the
        // ASYNC_NESTABLE_END.
        for (let i = 0; i < timingsData.consoleTimings.length; i++) {
          assert.strictEqual(
              timingsData.consoleTimings[i].args.data.beginEvent.ph, Trace.Types.Events.Phase.ASYNC_NESTABLE_START);
          assert.strictEqual(
              timingsData.consoleTimings[i].args.data.endEvent.ph, Trace.Types.Events.Phase.ASYNC_NESTABLE_END);
        }
      });

      it('sorts the blocks to ensure they are in time order', async function() {
        const events = await TraceLoader.rawEvents(this, 'timings-track.json.gz');
        Trace.Handlers.ModelHandlers.UserTimings.reset();
        // Reverse the array so that the events are in the wrong order.
        // This _shouldn't_ ever happen in a real trace, but it's best for us to
        // sort the blocks once we've parsed them just in case.
        const reversed = events.slice().reverse();
        for (const event of reversed) {
          Trace.Handlers.ModelHandlers.UserTimings.handleEvent(event);
        }
        await Trace.Handlers.ModelHandlers.UserTimings.finalize();
        const data = Trace.Handlers.ModelHandlers.UserTimings.data();
        assert.lengthOf(data.consoleTimings, 3);
        assert.isTrue(data.consoleTimings[0].ts <= data.consoleTimings[1].ts);
        assert.isTrue(data.consoleTimings[1].ts <= data.consoleTimings[2].ts);
      });

      it('calculates the duration correctly from the begin/end event timestamps', async () => {
        for (const consoleTiming of timingsData.consoleTimings) {
          // Ensure for each timing pair we've set the dur correctly.
          assert.strictEqual(
              consoleTiming.dur, consoleTiming.args.data.endEvent.ts - consoleTiming.args.data.beginEvent.ts);
        }
      });
    });
    describe('console.timestamp events parsing', function() {
      it('parses console.timestamp events correctly', async function() {
        assert.lengthOf(timingsData.timestampEvents, 3);
        assert.strictEqual(timingsData.timestampEvents[0].args.data?.message, 'a timestamp');
        assert.strictEqual(timingsData.timestampEvents[1].args.data?.message, 'another timestamp');
        assert.strictEqual(timingsData.timestampEvents[2].args.data?.message, 'yet another timestamp');
      });
    });
  });

  describe('UserTiming::Measure events parsing', function() {
    it('stores user timing events by trace id', async function() {
      const userTimingMeasure = makeCompleteEvent(Trace.Types.Events.Name.USER_TIMING_MEASURE, 0, 100, 'cat', 0, 0) as
          Trace.Types.Events.UserTimingMeasure;
      userTimingMeasure.args.traceId = 1;
      Trace.Handlers.ModelHandlers.UserTimings.handleEvent(userTimingMeasure);
      await Trace.Handlers.ModelHandlers.UserTimings.finalize();
      const data = Trace.Handlers.ModelHandlers.UserTimings.data();
      assert.lengthOf(data.measureTraceByTraceId, 1);
      assert.deepEqual([...data.measureTraceByTraceId.entries()][0], [1, userTimingMeasure]);
    });
  });
});
