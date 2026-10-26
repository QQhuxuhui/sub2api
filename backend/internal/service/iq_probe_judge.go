package service

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// iqProbeAnswerInstruction is appended to every probe question so the final
// answer can be told apart from numbers that appear in the reasoning.
const iqProbeAnswerInstruction = "\n\n请在回答的最后单独一行输出：答案：<你的最终答案>"

// iqProbeAnswerMarker matches the answer label; the last one in the reply wins.
var iqProbeAnswerMarker = regexp.MustCompile(`(?i)(答案|final answer|answer)\s*[:：]`)

const iqProbeNumericLiteral = `[+-]?(?:(?:\d{1,3}(?:,\d{3})+|\d+)(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`

// Match a complete number or fraction, with an optional trailing unit. Requiring
// whitespace before Latin units keeps an incomplete exponent ("1e") invalid.
var iqProbeNumberPattern = regexp.MustCompile(`^(` + iqProbeNumericLiteral + `)(?:\s*/\s*(` + iqProbeNumericLiteral + `))?(?:\s*\p{Han}+|\s+[a-zA-Z]+)?$`)

// iqProbeAlternativesSep separates several accepted answers ("42||四十二").
const iqProbeAlternativesSep = "||"

// buildIQProbePrompt returns the prompt actually sent for a question.
func buildIQProbePrompt(prompt string) string {
	return strings.TrimRight(prompt, " \t\r\n") + iqProbeAnswerInstruction
}

// extractIQProbeAnswer returns the text after the last answer label, up to the
// end of that line. ok is false when the reply has no label (format error).
func extractIQProbeAnswer(text string) (string, bool) {
	locs := iqProbeAnswerMarker.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return "", false
	}
	rest := text[locs[len(locs)-1][1]:]
	rest = strings.TrimLeft(rest, " \t")
	if i := strings.IndexAny(rest, "\r\n"); i >= 0 {
		// "答案：\n42" — the answer may sit on the next line.
		if strings.TrimSpace(rest[:i]) == "" {
			rest = strings.TrimLeft(rest[i:], "\r\n \t")
			if j := strings.IndexAny(rest, "\r\n"); j >= 0 {
				rest = rest[:j]
			}
		} else {
			rest = rest[:i]
		}
	}
	answer := cleanIQProbeAnswer(rest)
	if answer == "" {
		return "", false
	}
	return answer, true
}

// cleanIQProbeAnswer strips markdown emphasis, code ticks, LaTeX boxes and
// trailing punctuation that models wrap around a final answer.
func cleanIQProbeAnswer(s string) string {
	s = strings.TrimSpace(s)
	for _, w := range []string{"**", "__", "`", "$"} {
		s = strings.ReplaceAll(s, w, "")
	}
	if strings.HasPrefix(s, `\boxed{`) && strings.HasSuffix(s, "}") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, `\boxed{`), "}")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return strings.ContainsRune("。.，,；;！!", r) || unicode.IsSpace(r)
	})
	return strings.TrimSpace(s)
}

func normalizeIQProbeExact(s string) string {
	s = strings.ToLower(cleanIQProbeAnswer(s))
	s = strings.Trim(s, `"'“”‘’「」『』`)
	return strings.Join(strings.Fields(s), " ")
}

// iqProbeChoiceLetters reads the leading option letters: "(B) Paris" → "B",
// "A、C" → "AC". Letters are sorted so multi-select order does not matter.
func iqProbeChoiceLetters(s string) string {
	s = strings.TrimSpace(cleanIQProbeAnswer(s))
	s = strings.TrimLeft(s, "([（【 ")
	s = strings.TrimPrefix(s, "选项")
	var letters []rune
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			// A letter followed by a lowercase letter starts a word ("Both"), not
			// an option list ("AC").
			if i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z' {
				return sortedLetters(letters)
			}
			letters = append(letters, unicode.ToUpper(r))
		case strings.ContainsRune(",，、 和与", r):
			continue
		default:
			return sortedLetters(letters)
		}
	}
	return sortedLetters(letters)
}

func sortedLetters(letters []rune) string {
	seen := map[rune]bool{}
	out := make([]rune, 0, len(letters))
	for c := 'A'; c <= 'Z'; c++ {
		for _, l := range letters {
			if l == c && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return string(out)
}

func parseIQProbeNumber(s string) (float64, bool) {
	m := iqProbeNumberPattern.FindStringSubmatch(cleanIQProbeAnswer(s))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0, false
	}
	if m[2] != "" {
		denominator, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", ""), 64)
		if err != nil || denominator == 0 {
			return 0, false
		}
		v /= denominator
	}
	return v, !math.IsNaN(v) && !math.IsInf(v, 0)
}

// judgeIQProbeAnswer compares an extracted answer against the expected one.
// expected may list alternatives separated by "||".
func judgeIQProbeAnswer(judgeType, expected string, tolerance float64, extracted string) bool {
	for _, alt := range strings.Split(expected, iqProbeAlternativesSep) {
		if strings.TrimSpace(alt) == "" {
			continue
		}
		if judgeIQProbeSingle(judgeType, alt, tolerance, extracted) {
			return true
		}
	}
	return false
}

func judgeIQProbeSingle(judgeType, expected string, tolerance float64, extracted string) bool {
	switch judgeType {
	case IQProbeJudgeChoice:
		want := iqProbeChoiceLetters(expected)
		return want != "" && iqProbeChoiceLetters(extracted) == want
	case IQProbeJudgeNumeric:
		want, ok := parseIQProbeNumber(expected)
		if !ok {
			return false
		}
		got, ok := parseIQProbeNumber(extracted)
		if !ok {
			return false
		}
		return math.Abs(got-want) <= math.Max(tolerance, 1e-9)
	default:
		return normalizeIQProbeExact(extracted) == normalizeIQProbeExact(expected)
	}
}

// iqProbeVerdict turns one probe result into a verdict plus the extracted answer.
func iqProbeVerdict(q *IQProbeQuestion, res *AccountProbeResult) (verdict, extracted string) {
	switch {
	case res.Unsupported:
		return IQProbeVerdictUnsupported, ""
	case !res.Success:
		return IQProbeVerdictError, ""
	}
	extracted, ok := extractIQProbeAnswer(res.ResponseText)
	if !ok {
		return IQProbeVerdictFormatError, ""
	}
	if judgeIQProbeAnswer(q.JudgeType, q.ExpectedAnswer, q.NumericTolerance, extracted) {
		return IQProbeVerdictCorrect, extracted
	}
	return IQProbeVerdictWrong, extracted
}

// iqProbeScoreRun fills the run's counters and score from its answers. Request
// failures stay out of the denominator; when more than half of the questions
// failed the round is invalid and carries no score.
func iqProbeScoreRun(run *IQProbeRun, answers []*IQProbeAnswer) {
	run.TotalCount = len(answers)
	run.CorrectCount, run.WrongCount, run.FormatErrorCount, run.ErrorCount = 0, 0, 0, 0
	run.EarnedWeight, run.TotalWeight = 0, 0
	for _, a := range answers {
		switch a.Verdict {
		case IQProbeVerdictCorrect:
			run.CorrectCount++
			run.EarnedWeight += a.Weight
			run.TotalWeight += a.Weight
		case IQProbeVerdictWrong:
			run.WrongCount++
			run.TotalWeight += a.Weight
		case IQProbeVerdictFormatError:
			run.FormatErrorCount++
			run.TotalWeight += a.Weight
		default:
			run.ErrorCount++
		}
	}
	if run.TotalCount == 0 || run.ErrorCount*2 > run.TotalCount || run.TotalWeight <= 0 {
		run.Status = IQProbeRunInvalid
		run.Score = nil
		return
	}
	score := run.EarnedWeight / run.TotalWeight
	run.Status = IQProbeRunScored
	run.Score = &score
}
