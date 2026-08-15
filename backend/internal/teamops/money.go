package teamops

import (
	"math"
	"sort"
)

// roundCents 把金额取整到 2 位小数——页面上金额的唯一渲染精度。
// 单个数字的取整用它，一组数字的取整必须用 AllocateDisplay：
// 各行分别 roundCents 之后加起来不等于总额 roundCents 的概率是 59.2%。
func roundCents(f float64) float64 { return math.Round(f*100) / 100 }

// AllocateDisplay 把各行金额取整到 2 位小数，并把
//
//	round(total) - Σround(row)
//
// 的差额按最大余数法摊回各行，保证：
//
//	Σ 各行展示金额 == 展示总额
//
// 为什么必须做：10 行两位小数时，Σround(行) ≠ round(Σ) 的概率实测 59.2%。
// 页面底部对账条那句「N 行合计 ＝ 本期全部消耗」是产品承诺，不做分配的话，
// 一上线就有六成机会被用户拿计算器当场证伪。这不是浮点误差，提高精度救不了。
//
// 摊给谁：先按**取整残差** value*100 - round(value*100)（落在 [-0.5, 0.5]）排序，
// 补钱时残差大的优先、扣钱时残差小的优先；残差相同的摊给金额大的行——
// 一分钱落在大数上最不显眼。
//
// 残差为正说明这一行被取整**截掉**了钱，补给它最合理；为负说明它已经被**进位**
// 多算了钱，扣它最合理。这里不能换成教科书里配 floor 用的「小数部分」：各行先走的是
// round 不是 floor，小数部分 0.6 的那一行早已被进位过一次，再补一分会让它偏离原值
// 1.4 分，比不分配还差。按残差排则每一行的展示值与原值最多差 1 分。
//
// 返回值与 values 等长、逐位对应（调用方靠下标把金额写回对应的行），values 本身不被修改。
func AllocateDisplay(total float64, values []float64) []float64 {
	if len(values) == 0 {
		return nil
	}

	cents := make([]int64, len(values))
	residual := make([]float64, len(values))
	var sumCents int64
	for i, v := range values {
		scaled := v * 100
		c := int64(math.Round(scaled))
		cents[i] = c
		residual[i] = scaled - float64(c)
		sumCents += c
	}

	// diff 在正常调用下是 ±1~±3 分：total 与 values 出自同一次聚合，
	// 差额只可能来自各行自己的取整。
	if diff := int64(math.Round(total*100)) - sumCents; diff != 0 {
		idx := make([]int, len(values))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			ra, rb := residual[idx[a]], residual[idx[b]]
			if ra != rb {
				if diff > 0 {
					return ra > rb // 补钱给被截得最狠的行
				}
				return ra < rb // 扣钱从被进位最多的行开始
			}
			return values[idx[a]] > values[idx[b]]
		})

		// 先均摊 diff/n，再把余下的 |diff%n| 分按上面的顺序发给最靠前的几行，
		// 两步加起来正好是 diff。正常调用下 |diff| < 行数，均摊那一步是 0，
		// 等价于「给最靠前的 |diff| 行各 ±1 分」。
		//
		// 之所以不写成「逐行 ±1 分、发完为止」：那种写法在 |diff| 大于行数时会剩下一截
		// 差额没人认领，恒等式无声地断掉——而恒等式是这个函数存在的唯一理由。
		// total 与 values 由两条 SQL 分别算出，同源是调用方的约定，不是这里的前提。
		n := int64(len(values))
		base, rest := diff/n, diff%n
		step := int64(1)
		if rest < 0 {
			step, rest = -1, -rest
		}
		for k, i := range idx {
			cents[i] += base
			if int64(k) < rest {
				cents[i] += step
			}
		}
	}

	out := make([]float64, len(values))
	for i, c := range cents {
		out[i] = float64(c) / 100
	}
	return out
}
