export default {
  openaiCacheUser: {
    tabs: { accounts: '按账号', users: '按用户' },
    global: {
      title: '按用户缓存模拟',
      hint: '开启后，没有单独配置的用户在 Responses 接口按用户拉到区间；新对话与零缓存请求不加码。用户单独配置优先。',
      overrides: '{n} 个用户单独配置',
      overridesHint: '点击只看有单独配置的用户。完整回滚 = 关闭这里 + 把单独配置逐个改回继承。'
    },
    onlyOverrides: '仅看单独配置',
    timezoneHint: '统计时区：{tz}（与验收口径一致，不随浏览器时区变化）',
    trendFor: '{name} 的缓存走势',
    edit: '配置',
    editHint: '设置该用户的单独配置（继承 / 开启 / 关闭）',
    table: {
      user: '用户',
      source: '生效',
      sourceHint: '用户单独配置 > 全局 > 账号自身模拟',
      reachable: '可达',
      reachableHint: '按请求中上一轮输入算出的真实上限；未按用户模拟的行按实际缓存计。',
      coverage: '覆盖率 {pct}',
      verdict: '达标'
    },
    source: {
      userOn: '单独开启',
      userOff: '单独关闭',
      global: '全局',
      account: '随账号',
      accountHint: '未按用户模拟，沿用账号自身的模拟'
    },
    verdict: { ok: '达标', ceiling_short: '上限不足', strategy_short: '未达标' },
    verdictHint: {
      ok: '展示缓存率 ≥ 区间下限',
      ceiling_short: '真实可达上限本就低于下限（新对话、零缓存多）',
      strategy_short: '可达但未拉到下限；小时样本少时可能受 128 粒度限制（按当前配置判断）'
    },
    verdictEmpty: { off: '已关闭', account: '随账号', noInput: '无输入用量' },
    modal: {
      title: '{name} 的缓存模拟',
      mode: { inherit: '继承', on: '单独开启', off: '关闭' },
      hint: {
        inherit: '删除单独配置，按全局（全局关时按账号）。',
        on: '该用户按下面的区间拉齐，不受全局开关影响。',
        off: '该用户所有接口都不加码，账号模拟也不生效。'
      }
    }
  }
}
