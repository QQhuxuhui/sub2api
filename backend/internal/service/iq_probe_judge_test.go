//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractIQProbeAnswer(t *testing.T) {
	cases := []struct {
		name, text, want string
		ok               bool
	}{
		{"last label wins", "先算 答案：3 不对\n重新算……\n答案：42", "42", true},
		{"ascii colon", "reasoning\nAnswer: B", "B", true},
		{"final answer", "...\nFinal Answer: **17**.", "17", true},
		{"markdown wrapped", "答案：**`Paris`**。", "Paris", true},
		{"next line", "答案：\n  42\n", "42", true},
		{"boxed", "答案：$\\boxed{7}$", "7", true},
		{"no label", "我认为是 42", "", false},
		{"empty after label", "答案：   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractIQProbeAnswer(tc.text)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestJudgeIQProbeAnswer(t *testing.T) {
	cases := []struct {
		name, judge, expected string
		tol                   float64
		got                   string
		want                  bool
	}{
		{"exact case/space", IQProbeJudgeExact, "New  York", 0, "new york", true},
		{"exact quotes", IQProbeJudgeExact, "苹果", 0, "「苹果」", true},
		{"exact mismatch", IQProbeJudgeExact, "苹果", 0, "香蕉", false},
		{"exact alternatives", IQProbeJudgeExact, "42||四十二", 0, "四十二", true},
		{"choice bracket", IQProbeJudgeChoice, "B", 0, "(B) Paris", true},
		{"choice lower", IQProbeJudgeChoice, "B", 0, "b", true},
		{"choice multi order", IQProbeJudgeChoice, "AC", 0, "C、A", true},
		{"choice multi missing", IQProbeJudgeChoice, "AC", 0, "A", false},
		{"choice word is not option", IQProbeJudgeChoice, "B", 0, "Both", false},
		{"choice prefix", IQProbeJudgeChoice, "D", 0, "选项D", true},
		{"numeric float", IQProbeJudgeNumeric, "42", 0, "42.0", true},
		{"numeric unit", IQProbeJudgeNumeric, "1250", 0, "1,250 元", true},
		{"numeric tolerance", IQProbeJudgeNumeric, "3.1416", 0.001, "3.1415", true},
		{"numeric off", IQProbeJudgeNumeric, "3.1416", 0.00001, "3.14", false},
		{"numeric negative", IQProbeJudgeNumeric, "-7", 0, "-7", true},
		{"numeric no number", IQProbeJudgeNumeric, "7", 0, "seven", false},
		{"numeric exponent", IQProbeJudgeNumeric, "1000", 0, "1e3", true},
		{"numeric exponent expected", IQProbeJudgeNumeric, "1e3", 0, "1000", true},
		{"numeric exponent not prefix", IQProbeJudgeNumeric, "1", 0, "1e3", false},
		{"numeric signed exponent", IQProbeJudgeNumeric, "-0.0012", 0, "-1.2E-3", true},
		{"numeric fraction", IQProbeJudgeNumeric, "0.5", 0, "1 / 2", true},
		{"numeric fraction not numerator", IQProbeJudgeNumeric, "1", 0, "1/2", false},
		{"numeric zero denominator", IQProbeJudgeNumeric, "1", 0, "1/0", false},
		{"numeric expression", IQProbeJudgeNumeric, "1", 0, "1+2", false},
		{"numeric incomplete exponent", IQProbeJudgeNumeric, "1", 0, "1e", false},
		{"numeric multiple answers", IQProbeJudgeNumeric, "1", 0, "1 or 2", false},
		{"numeric malformed grouping", IQProbeJudgeNumeric, "123", 0, "1,2,3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, judgeIQProbeAnswer(tc.judge, tc.expected, tc.tol, tc.got))
		})
	}
}

func TestIQProbeVerdict(t *testing.T) {
	q := &IQProbeQuestion{JudgeType: IQProbeJudgeNumeric, ExpectedAnswer: "42"}
	v, ex := iqProbeVerdict(q, &AccountProbeResult{Success: true, ResponseText: "推理里出现 42\n答案：41"})
	require.Equal(t, IQProbeVerdictWrong, v)
	require.Equal(t, "41", ex)

	v, _ = iqProbeVerdict(q, &AccountProbeResult{Success: true, ResponseText: "就是 42"})
	require.Equal(t, IQProbeVerdictFormatError, v)

	v, _ = iqProbeVerdict(q, &AccountProbeResult{ErrorMessage: "API returned 500"})
	require.Equal(t, IQProbeVerdictError, v)

	v, _ = iqProbeVerdict(q, &AccountProbeResult{Unsupported: true})
	require.Equal(t, IQProbeVerdictUnsupported, v)
}

func TestIQProbeScoreRun(t *testing.T) {
	ans := func(verdict string, w float64) *IQProbeAnswer { return &IQProbeAnswer{Verdict: verdict, Weight: w} }

	run := &IQProbeRun{}
	iqProbeScoreRun(run, []*IQProbeAnswer{
		ans(IQProbeVerdictCorrect, 2),
		ans(IQProbeVerdictWrong, 1),
		ans(IQProbeVerdictFormatError, 1),
		ans(IQProbeVerdictError, 5),
	})
	require.Equal(t, IQProbeRunScored, run.Status)
	require.NotNil(t, run.Score)
	require.InDelta(t, 0.5, *run.Score, 1e-9) // errors stay out of the denominator
	require.Equal(t, 1, run.FormatErrorCount)
	require.Equal(t, 1, run.ErrorCount)

	run = &IQProbeRun{}
	iqProbeScoreRun(run, []*IQProbeAnswer{
		ans(IQProbeVerdictCorrect, 1),
		ans(IQProbeVerdictError, 1),
		ans(IQProbeVerdictUnsupported, 1),
	})
	require.Equal(t, IQProbeRunInvalid, run.Status)
	require.Nil(t, run.Score)

	run = &IQProbeRun{}
	iqProbeScoreRun(run, []*IQProbeAnswer{ans(IQProbeVerdictCorrect, 1), ans(IQProbeVerdictError, 1)})
	require.Equal(t, IQProbeRunScored, run.Status, "exactly half failing is still scored")
}

func TestNextIQProbeAlertState(t *testing.T) {
	score := func(v float64) *float64 { return &v }
	m := &IQProbeMonitor{AlertEnabled: true, AlertThreshold: 0.7, AlertLowRounds: 2, AlertInvalidRounds: 3, AlertState: IQProbeAlertOK}
	apply := func(run *IQProbeRun) string {
		st, alert := nextIQProbeAlertState(m, run)
		m.ConsecutiveLow, m.ConsecutiveInvalid, m.AlertState, m.LastScore = st.ConsecutiveLow, st.ConsecutiveInvalid, st.AlertState, st.LastScore
		return alert
	}
	low := &IQProbeRun{Status: IQProbeRunScored, Score: score(0.4)}
	good := &IQProbeRun{Status: IQProbeRunScored, Score: score(0.9)}
	invalid := &IQProbeRun{Status: IQProbeRunInvalid}
	skipped := &IQProbeRun{Status: IQProbeRunSkipped}

	require.Equal(t, "", apply(low))
	require.Equal(t, "", apply(skipped), "skipped rounds do not break the streak")
	require.Equal(t, IQProbeAlertLow, apply(low))
	require.Equal(t, "", apply(low), "no repeat while still low")
	require.Equal(t, IQProbeAlertOK, apply(good))
	require.Equal(t, "", apply(good))

	require.Equal(t, "", apply(invalid))
	require.Equal(t, "", apply(invalid))
	require.Equal(t, IQProbeAlertInvalid, apply(invalid))
	require.Equal(t, "", apply(invalid))
	require.Equal(t, IQProbeAlertOK, apply(good))

	m.AlertEnabled = false
	require.Equal(t, "", apply(low))
	require.Equal(t, "", apply(low))
	require.Equal(t, IQProbeAlertOK, m.AlertState)
	require.Equal(t, 2, m.ConsecutiveLow, "streaks still tracked while alerts are off")
}

func TestNormalizeIQProbeQuestion(t *testing.T) {
	q := &IQProbeQuestion{Prompt: " p ", ExpectedAnswer: "B", JudgeType: "Choice"}
	require.NoError(t, normalizeIQProbeQuestion(q))
	require.Equal(t, IQProbeJudgeChoice, q.JudgeType)
	require.Equal(t, 1.0, q.Weight)

	require.Error(t, normalizeIQProbeQuestion(&IQProbeQuestion{Prompt: "p", ExpectedAnswer: "abc", JudgeType: IQProbeJudgeNumeric}))
	require.Error(t, normalizeIQProbeQuestion(&IQProbeQuestion{Prompt: "p", ExpectedAnswer: "巴黎", JudgeType: IQProbeJudgeChoice}))
	require.Error(t, normalizeIQProbeQuestion(&IQProbeQuestion{Prompt: "", ExpectedAnswer: "1"}))
	require.Error(t, normalizeIQProbeQuestion(&IQProbeQuestion{Prompt: "p", ExpectedAnswer: "1", JudgeType: "regex"}))
}

func TestNextIQProbeAlertState_LowThenInvalid(t *testing.T) {
	m := &IQProbeMonitor{AlertEnabled: true, AlertThreshold: 0.7, AlertLowRounds: 2, AlertInvalidRounds: 3, AlertState: IQProbeAlertOK}
	score := 0.4
	for i, want := range []string{"", IQProbeAlertLow, "", "", IQProbeAlertInvalid, "", ""} {
		run := &IQProbeRun{Status: IQProbeRunInvalid}
		if i < 2 {
			run.Status, run.Score = IQProbeRunScored, &score
		}
		st, alert := nextIQProbeAlertState(m, run)
		require.Equal(t, want, alert, "round %d must only alert for its own status", i+1)
		m.ConsecutiveLow, m.ConsecutiveInvalid, m.AlertState, m.LastScore = st.ConsecutiveLow, st.ConsecutiveInvalid, st.AlertState, st.LastScore
	}
}
