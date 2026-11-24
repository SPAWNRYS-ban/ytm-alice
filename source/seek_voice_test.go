package main

import "testing"

func TestParseSeekVoice(t *testing.T) {
	cases := []struct {
		text string
		want seekSpec
		ok   bool
	}{
		{"перемотай на 30 секунд", seekSpec{Relative: true, OffsetMS: 30000}, true},
		{"перемотай вперёд на 1 минуту", seekSpec{Relative: true, OffsetMS: 60000}, true},
		{"перемотай назад на 1 минуту 30 секунд", seekSpec{Relative: true, OffsetMS: -90000}, true},
		{"перемотай на 45 секунд назад", seekSpec{Relative: true, OffsetMS: -45000}, true},
		{"перемотай на 1:30", seekSpec{PositionMS: 90000}, true},
		{"перемотай на 1:65", seekSpec{}, false},
		{"перемотай на 2 минуты 30 секунд", seekSpec{PositionMS: 150000}, true},
		{"перемотай на две минуты двадцать пять секунд", seekSpec{PositionMS: 145000}, true},
		{"перемотай назад на одну минуту", seekSpec{Relative: true, OffsetMS: -60000}, true},
		{"перемотай на 999999999999999999 минут", seekSpec{}, false},
		{"перемотай на 30-ю секунду", seekSpec{PositionMS: 30000}, true},
		{"перемотай на вторую минуту", seekSpec{PositionMS: 120000}, true},
		{"перемотай на тридцатую секунду", seekSpec{PositionMS: 30000}, true},
		{"перемотай на двадцать пятую секунду", seekSpec{PositionMS: 25000}, true},
		{"перемотай на сороковую минуту", seekSpec{PositionMS: 2400000}, true},
		{"перемотай на тридцать секунд", seekSpec{Relative: true, OffsetMS: 30000}, true},
		{"перемотай на 25 секунду", seekSpec{PositionMS: 25000}, true},
		{"перемотай на 30 секунду", seekSpec{PositionMS: 30000}, true},
		{"перемотай на 5 минуту", seekSpec{PositionMS: 300000}, true},
		{"перемотай на 2 минуту", seekSpec{PositionMS: 120000}, true},
		{"перемотай на 30 секунд", seekSpec{Relative: true, OffsetMS: 30000}, true},
		{"перемотай на 1 минуту", seekSpec{Relative: true, OffsetMS: 60000}, true},
		{"перемотай на 25 секунд назад", seekSpec{Relative: true, OffsetMS: -25000}, true},
		{"перемотай на 5 минуту 30 секунду", seekSpec{PositionMS: 330000}, true},
		{"перемотай на 1 минуту 30 секунду", seekSpec{PositionMS: 90000}, true},
		{"перемотай в начало", seekSpec{PositionMS: 0}, true},
		{"Алиса, перемотай на минуту, пожалуйста", seekSpec{Relative: true, OffsetMS: 60000}, true},
		{"перемотай на ютубе трек", seekSpec{}, false},
		{"включи музыку", seekSpec{}, false},
		{"перемотай", seekSpec{}, false},
	}
	for _, tc := range cases {
		got, ok := parseSeekVoice(tc.text)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%q: got %+v ok=%v, want %+v ok=%v", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestVoiceActionRoutesSeek(t *testing.T) {
	if a, q, ok := voiceAction("Алиса, перемотай на 30 секунд"); !ok || a != "seek" || q != "" {
		t.Fatalf("got a=%q q=%q ok=%v", a, q, ok)
	}
	if a, _, ok := voiceAction("включи музыку"); ok && a == "seek" {
		t.Fatal("ordinary music request must not be a seek")
	}
}

func TestSeekSpecTarget(t *testing.T) {
	cases := []struct {
		spec         seekSpec
		current, dur int64
		want         int64
	}{
		{seekSpec{PositionMS: 120000}, 10000, 100000, 99000},
		{seekSpec{Relative: true, OffsetMS: -30000}, 10000, 100000, 0},
		{seekSpec{Relative: true, OffsetMS: 30000}, 80000, 100000, 99000},
		{seekSpec{PositionMS: 0}, 50000, 100000, 0},
		{seekSpec{PositionMS: 50000}, 0, 0, 50000},
		{seekSpec{PositionMS: 99500}, 0, 100000, 99000},
	}
	for _, tc := range cases {
		if got := tc.spec.target(tc.current, tc.dur); got != tc.want {
			t.Errorf("%+v current=%d dur=%d: got %d, want %d", tc.spec, tc.current, tc.dur, got, tc.want)
		}
	}
}
