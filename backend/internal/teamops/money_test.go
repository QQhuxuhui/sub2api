//go:build unit

package teamops

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// displayCents 把金额换算成页面上真正渲染出来的「分」。
// 断言一律落在整数分上：要守的是「Σ各行展示金额 == 对账条展示金额」这条**像素级**恒等式
// ——两边渲染出来的两位小数逐位相等，不是「差得够小」。容差断言在这里守不住东西：
// 差一分正好是 0.01，比任何合理的容差都大，也比任何浮点误差都大。
func displayCents(amount float64) int64 { return int64(math.Round(amount * 100)) }

func sumDisplayCents(amounts []float64) int64 {
	var total int64
	for _, a := range amounts {
		total += displayCents(a)
	}
	return total
}

func TestAllocateDisplay_SumEqualsRoundedTotal(t *testing.T) {
	t.Parallel()
	// 三行各 0.005：各自四舍五入成 0.01，加起来 0.03，而总额 0.015 只显示成 0.02。
	vals := []float64{0.005, 0.005, 0.005}
	total := 0.015

	// 选例前提。没有这一条，把 vals 换成一组「本来就对得上」的数字，
	// 下面那条断言在完全不做分配的实现上照样绿 —— 那样这个测试证明不了分配逻辑存在。
	require.NotEqual(t, displayCents(total), sumDisplayCents(vals),
		"这组数字必须真的产生差额，否则本用例没有鉴别力")

	require.Equal(t, displayCents(total), sumDisplayCents(AllocateDisplay(total, vals)),
		"Σ各行展示金额必须等于展示总额（这是页面对账条上的产品承诺）")
}

// displayCase 是一组「总额 + 各行金额」。
type displayCase struct {
	total  float64
	values []float64
}

// randomizedCases 生成 200 组固定种子的伪随机金额，行数 3~14。
//
// 金额取到**千分位**是这套用例有没有牙的关键：如果生成的金额本身就只有两位小数，
// round 前后完全一样，200 组里一组差额都不会出现，循环跑得再多也只是在验证
// 「不需要分配时不要乱动」，分配逻辑整段删掉照样全绿。实测千分位下 109/200 组需要分配
// （下面 TestAllocateDisplay_RandomizedSumEqualsRoundedTotal 把这个下限也钉住了）。
func randomizedCases() []displayCase {
	seed := uint64(20260815)
	next := func() float64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return float64(seed%1000000) / 1000.0
	}

	cases := make([]displayCase, 0, 200)
	for c := 0; c < 200; c++ {
		values := make([]float64, 3+c%12)
		var total float64
		for i := range values {
			values[i] = next()
			total += values[i]
		}
		cases = append(cases, displayCase{total: total, values: values})
	}
	return cases
}

func TestAllocateDisplay_RandomizedSumEqualsRoundedTotal(t *testing.T) {
	t.Parallel()

	needsAllocation := 0
	for i, tc := range randomizedCases() {
		if sumDisplayCents(tc.values) != displayCents(tc.total) {
			needsAllocation++
		}
		require.Equal(t, displayCents(tc.total), sumDisplayCents(AllocateDisplay(tc.total, tc.values)),
			"第 %d 组不守恒", i)
	}
	require.Greater(t, needsAllocation, 50,
		"200 组里真正需要分配的只有 %d 组 —— 用例集自己失去了鉴别力，先修生成器再看结论", needsAllocation)
}

func TestAllocateDisplay_RandomizedRowStaysWithinOneCent(t *testing.T) {
	t.Parallel()

	// 恒等式可以用「把差额全砸给第一行」满足，但那一行会显示成一个谁也对不上的数。
	// 差额必须摊给**被取整截掉最多**的那几行，每一行的展示值才不会偏离自己的原值超过 1 分。
	for i, tc := range randomizedCases() {
		got := AllocateDisplay(tc.total, tc.values)
		require.Len(t, got, len(tc.values), "第 %d 组行数变了", i)
		for j, v := range got {
			require.InDelta(t, tc.values[j], v, 0.01+1e-9,
				"第 %d 组第 %d 行展示成 %v，原值 %v，偏离超过 1 分", i, j, v, tc.values[j])
		}
	}
}

func TestAllocateDisplay_SurplusGoesToTheMostRoundedDownRow(t *testing.T) {
	t.Parallel()

	// 残差（value*100 - round(value*100)）依次是 -0.4 / +0.1 / +0.49 / +0.48：
	// 只有 1.006 是被**进位**多算了钱的，另外三行都被截掉了钱。
	// 多出来的这 1 分必须补给被截得最狠的 3.0049。
	//
	// 若改按「小数部分最大」排（把 round 的残差换成 floor 的小数部分，这是最大余数法
	// 用在 floor 上的写法，用在 round 上就错了），1.006 的小数部分 0.6 反而排第一，
	// 它会被显示成 1.02 —— 与真值差 1.4 分，还不如不分配。
	got := AllocateDisplay(10.0167, []float64{1.006, 2.001, 3.0049, 4.0048})
	require.Equal(t, []float64{1.01, 2.00, 3.01, 4.00}, got)
}

func TestAllocateDisplay_ShortfallComesFromTheMostRoundedUpRow(t *testing.T) {
	t.Parallel()

	// 残差依次是 +0.4 / -0.1 / -0.49 / -0.48：少的这 1 分要从被进位最多的 3.0051 身上扣，
	// 而不是从唯一一个被截掉钱的 1.004 身上扣（那会把它显示成 0.99）。
	got := AllocateDisplay(10.0233, []float64{1.004, 2.009, 3.0051, 4.0052})
	require.Equal(t, []float64{1.00, 2.01, 3.00, 4.01}, got)
}

func TestAllocateDisplay_TieGoesToTheLargerRow(t *testing.T) {
	t.Parallel()

	// 1.125 与 3.125 的残差都正好是 -0.5（两个数在二进制里都是精确的，不存在浮点抖动），
	// 该扣谁只能由第二关键字决定：摊给金额大的那一行 —— 1 分钱落在大数上最不显眼。
	// 少了第二关键字，扣的就是排在前面的 1.125，结果变成 [1.12 3.13]。
	got := AllocateDisplay(4.25, []float64{1.125, 3.125})
	require.Equal(t, []float64{1.13, 3.12}, got)
}

func TestAllocateDisplay_KeepsRowOrder(t *testing.T) {
	t.Parallel()

	// 返回值必须与入参**逐位对应**。实现内部要排序，一旦把排序结果直接吐出去，
	// 页面上就会出现「张三那一行显示的是李四的钱」——恒等式还是成立的，账全错。
	// 这组金额本身就是两位小数，不需要分配，正确实现原样返回。
	vals := []float64{7.01, 1.02, 4.03}
	require.Equal(t, []float64{7.01, 1.02, 4.03}, AllocateDisplay(12.06, vals))
}

func TestAllocateDisplay_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	vals := []float64{0.005, 0.005, 0.005}
	AllocateDisplay(0.015, vals)
	require.Equal(t, []float64{0.005, 0.005, 0.005}, vals,
		"调用方还要用原始金额算占比、环比，入参不能被就地改写")
}

func TestAllocateDisplay_SingleRowAbsorbsTheWholeDiff(t *testing.T) {
	t.Parallel()

	// 只有一行时它就是全部：这一行的展示值必须等于展示总额，不能剩下 1 分没人认领。
	require.Equal(t, []float64{1.00}, AllocateDisplay(1.0, []float64{0.994}))
}

func TestAllocateDisplay_BalancesWhenDiffExceedsRowCount(t *testing.T) {
	t.Parallel()

	// total 与 values 由两条 SQL 分别算出，调用方保证它们同源，但仓储层不该依赖调用方：
	// 一旦两边真的分叉（过滤条件写歪、两次查询之间有新日志落库），差额就可能远大于行数。
	// 「每行至多 ±1 分」的摊法此时摊不完，会剩下一截差额，恒等式无声地断掉 ——
	// 而这条恒等式正是这个函数存在的唯一理由。
	total := 100.0
	got := AllocateDisplay(total, []float64{0.01, 0.02})
	require.Equal(t, displayCents(total), sumDisplayCents(got))
}

func TestAllocateDisplay_NoRowsReturnsNothing(t *testing.T) {
	t.Parallel()

	require.Empty(t, AllocateDisplay(0, nil))
	require.Empty(t, AllocateDisplay(12.34, []float64{}))
}

func TestAllocateDisplay_AllZeroRowsStayZero(t *testing.T) {
	t.Parallel()

	// 零消耗的令牌也要成行（见 ListRows 的搜索用例），这些行必须老老实实显示 0.00，
	// 不能因为「要凑总额」被塞进一分钱。
	require.Equal(t, []float64{0, 0}, AllocateDisplay(0, []float64{0, 0}))
}
