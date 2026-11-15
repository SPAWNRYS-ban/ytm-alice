package main

import (
	"regexp"
	"strconv"
	"strings"
)

type seekSpec struct {
	Relative   bool
	OffsetMS   int64
	PositionMS int64
}

var (
	seekClockRe   = regexp.MustCompile(`^(\d{1,2}):(\d{1,2})$`)
	seekOrdinalRe = regexp.MustCompile(`^(\d{1,3})-(?:ю|й|ую)$`)
)

// parseSeekVoice распознаёт «перемотай …» и возвращает позицию.
// Поддерживаются относительные фразы (вперёд/назад, bare «на N секунд»),
// абсолютные («на 1:30», «на 2 минуты 30 секунд», «на 30-ю секунду»,
// «на вторую минуту») и «в начало».
func parseSeekVoice(text string) (seekSpec, bool) {
	q := normalize(text)
	q = strings.TrimSpace(strings.TrimPrefix(q, "алиса "))
	q = strings.TrimSpace(strings.TrimSuffix(q, " пожалуйста"))
	stem := ""
	switch {
	case strings.HasPrefix(q, "перемотай"):
		stem = "перемотай"
	case strings.HasPrefix(q, "промотай"):
		stem = "промотай"
	default:
		return seekSpec{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(q, stem))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "на "))

	if rest == "в начало" || rest == "с начала" {
		return seekSpec{PositionMS: 0}, true
	}
	// Абсолютная позиция M:SS.
	if m := seekClockRe.FindStringSubmatch(rest); m != nil {
		min, _ := strconv.Atoi(m[1])
		sec, _ := strconv.Atoi(m[2])
		return seekSpec{PositionMS: int64(min*60+sec) * 1000}, true
	}
	// Порядковая позиция: «30-ю секунду», «вторую минуту».
	if pos, ok := parseOrdinalPosition(rest); ok {
		return seekSpec{PositionMS: pos}, true
	}
	rel, dir := seekDirection(rest)
	body := stripDirection(rest)
	ms, ok := parseSeekDuration(body)
	if !ok {
		return seekSpec{}, false
	}
	if rel {
		if dir < 0 {
			ms = -ms
		}
		return seekSpec{Relative: true, OffsetMS: ms}, true
	}
	// «N секунду»/«N минуту» без направления: ASR теряет порядковый суффикс,
	// поэтому это абсолютная позиция, а не относительная длительность.
	if pos, ok := parseSingularAbsolute(body); ok {
		return seekSpec{PositionMS: pos}, true
	}
	// «на N минут M секунд» (две единицы) — абсолютная позиция.
	if isTwoUnitDuration(body) {
		return seekSpec{PositionMS: ms}, true
	}
	// Одиночная единица без направления — относительная (разговорное значение).
	return seekSpec{Relative: true, OffsetMS: ms}, true
}

// target применяет смещение и обрезает позицию: [0, duration-1000].
func (s seekSpec) target(currentMS, durationMS int64) int64 {
	pos := s.PositionMS
	if s.Relative {
		pos = currentMS + s.OffsetMS
	}
	if durationMS > 0 && pos > durationMS-1000 {
		pos = durationMS - 1000
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}

// seekDirection находит направление перемотки: вперёд → +1, назад → −1.
func seekDirection(rest string) (bool, int) {
	for _, w := range strings.Fields(rest) {
		switch w {
		case "вперёд", "вперед":
			return true, 1
		case "назад":
			return true, -1
		}
	}
	return false, 0
}

// stripDirection убирает из фразы слова направления и предлог «на»,
// оставляя только длительность: «вперёд на 1 минуту» → «1 минуту».
func stripDirection(rest string) string {
	var kept []string
	for _, w := range strings.Fields(rest) {
		switch w {
		case "вперёд", "вперед", "назад", "на":
			continue
		}
		kept = append(kept, w)
	}
	return strings.Join(kept, " ")
}

var durationNumbers = map[string]int64{
	"ноль": 0, "десять": 10, "двадцать": 20, "тридцать": 30,
	"сорок": 40, "пятьдесят": 50, "шестьдесят": 60, "семьдесят": 70,
	"восемьдесят": 80, "девяносто": 90, "сто": 100,
}

// parseSeekDuration разбирает длительность в миллисекундах: «30 секунд»,
// «1 минуту 30 секунд», «полминуты», «полторы минуты». Числа — цифрами
// или словами (ноль … сто).
func parseSeekDuration(body string) (int64, bool) {
	if body == "" {
		return 0, false
	}
	switch body {
	case "полминуты":
		return 30 * 1000, true
	case "полторы минуты":
		return 90 * 1000, true
	}
	var total int64
	words := strings.Fields(body)
	for i := 0; i < len(words); {
		// Одиночная единица без числа читается как «одна»: «на минуту».
		if unit, ok := durationUnit(words[i]); ok {
			total += unit
			i++
			continue
		}
		n, ok := durationNumber(words[i])
		if !ok || i+1 >= len(words) {
			return 0, false
		}
		unit, ok := durationUnit(words[i+1])
		if !ok {
			return 0, false
		}
		total += n * unit
		i += 2
	}
	return total, true
}

// durationUnit возвращает длительность единицы в миллисекундах.
func durationUnit(word string) (int64, bool) {
	switch {
	case strings.HasPrefix(word, "минут"):
		return 60 * 1000, true
	case strings.HasPrefix(word, "секунд"):
		return 1000, true
	}
	return 0, false
}

// durationNumber разбирает число словами или цифрами.
func durationNumber(word string) (int64, bool) {
	if n, err := strconv.Atoi(word); err == nil {
		return int64(n), true
	}
	n, ok := durationNumbers[word]
	return n, ok
}

// parseSingularAbsolute читает «N секунду»/«N минуту» как абсолютную позицию.
// ASR теряет порядковый суффикс, поэтому форма единственного винительного
// падежа с числом N >= 2 означает позицию, а не относительную длительность.
func parseSingularAbsolute(body string) (int64, bool) {
	words := strings.Fields(body)
	if len(words) != 2 {
		return 0, false
	}
	var unitMS int64
	switch words[1] {
	case "секунду":
		unitMS = 1000
	case "минуту":
		unitMS = 60 * 1000
	default:
		return 0, false
	}
	n, ok := durationNumber(words[0])
	if !ok || n < 2 {
		return 0, false
	}
	return n * unitMS, true
}

// isTwoUnitDuration сообщает, что фраза содержит и минуты, и секунды.
func isTwoUnitDuration(body string) bool {
	hasMin, hasSec := false, false
	for _, w := range strings.Fields(body) {
		switch {
		case strings.HasPrefix(w, "минут"):
			hasMin = true
		case strings.HasPrefix(w, "секунд"):
			hasSec = true
		}
	}
	return hasMin && hasSec
}

var ordinalWords = map[string]int64{
	"первую": 1, "вторую": 2, "третью": 3, "четвёртую": 4, "пятую": 5,
	"шестую": 6, "седьмую": 7, "восьмую": 8, "девятую": 9, "десятую": 10,
	"одиннадцатую": 11, "двенадцатую": 12, "тринадцатую": 13,
	"четырнадцатую": 14, "пятнадцатую": 15, "шестнадцатую": 16,
	"семнадцатую": 17, "восемнадцатую": 18, "девятнадцатую": 19,
	"двадцатую": 20, "тридцатую": 30, "сороковую": 40, "пятидесятую": 50,
	"шестидесятую": 60, "семидесятую": 70, "восьмидесятую": 80,
	"девяностую": 90, "сотую": 100,
}

// parseOrdinalPosition разбирает порядковую позицию: «30-ю секунду» (30 с),
// «вторую минуту» (2·60 с), «двадцать пятую секунду» (25 с). Минуты считаются
// как N·60 — так же, как числовая форма «5 минуту» после потери суффикса ASR.
func parseOrdinalPosition(rest string) (int64, bool) {
	words := strings.Fields(rest)
	if len(words) != 2 && len(words) != 3 {
		return 0, false
	}
	var unitMS, adjustMS int64
	switch {
	case strings.HasPrefix(words[len(words)-1], "минут"):
		unitMS, adjustMS = 60*1000, 0
	case strings.HasPrefix(words[len(words)-1], "секунд"):
		unitMS, adjustMS = 1000, 0
	default:
		return 0, false
	}
	var n int64
	if len(words) == 3 {
		// Составное порядковое: «двадцать пятую» = 20 + 5.
		tens, ok := durationNumbers[words[0]]
		if !ok || tens < 10 || tens%10 != 0 {
			return 0, false
		}
		unit, ok := ordinalWords[words[1]]
		if !ok || unit < 1 || unit > 9 {
			return 0, false
		}
		n = tens + unit
	} else if m := seekOrdinalRe.FindStringSubmatch(words[0]); m != nil {
		v, err := strconv.Atoi(m[1])
		if err != nil || v < 1 {
			return 0, false
		}
		n = int64(v)
	} else if v, ok := ordinalWords[words[0]]; ok {
		n = v
	} else {
		return 0, false
	}
	return n*unitMS + adjustMS, true
}
