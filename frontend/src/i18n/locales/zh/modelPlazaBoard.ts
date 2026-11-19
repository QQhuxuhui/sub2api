/** 模型广场看板（features/model-plaza-board）文案。 */
export default {
  plazaBoard: {
    searchPlaceholder: '搜索模型，如 gpt-6.1、opus',
    view: { label: '视图', card: '卡片', list: '列表' },
    summary: { label: '按状态筛选', all: '全部模型' },
    state: { healthy: '正常', warning: '波动', critical: '异常', unknown: '暂无调用' },
    filters: {
      group: '分组',
      allGroups: '全部分组',
      findGroup: '找分组',
      models: '{count} 个模型',
      exclusive: '专属分组',
      vendor: '厂商',
      billing: '计费方式',
      all: '全部',
      token: '按量',
      request: '按次',
      otherVendor: '其他'
    },
    sort: {
      label: '排序',
      rank: '最多人用',
      name: '按名称',
      price: '价格从低到高',
      stable: '最稳定',
      fast: '响应最快'
    },
    count: '{models} 个模型 · {lines} 条线路',
    billing: { token: '按量计费', request: '按次计费', image: '按张计费', video: '视频计费' },
    recommended: '推荐线路',
    price: {
      input: '输入',
      output: '输出',
      cacheRead: '缓存读取',
      perMillion: '/ 1M',
      perRequest: '/ 次',
      perImage: '/ 张',
      single: '单次',
      from: '起',
      unset: '待配置',
      official: '官方 {input} / {output}'
    },
    group: '分组',
    availability: '可用',
    ttft: '首字',
    details: '详情',
    copy: '复制模型名',
    strip: '最近有调用的 24 个小时',
    sampleTitle: '{time} · 成功率 {rate}',
    idle: '近 7 天无调用',
    hoursAgo: '{n} 小时前',
    daysAgo: '{n} 天前',
    list: { model: '模型', status: '状态', strip: '近期状态' },
    drawer: {
      lines: '{count} 条线路',
      close: '关闭',
      rate: '倍率 ×{rate}',
      cheapest: '最便宜',
      steadiest: '最稳',
      fastest: '最快',
      fullPricing: '完整计费明细'
    },
    emptyFiltered: '没有符合条件的模型，试试清空搜索或换个筛选。'
  }
}
