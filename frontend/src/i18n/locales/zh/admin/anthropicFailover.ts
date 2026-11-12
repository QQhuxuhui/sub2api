export default {
  anthropicFailover: {
    title: '换号规则',
    description: '上游报错时按规则切换账号，不惩罚账号。',
    saved: '已保存',
    failed: '操作失败',
    statsWindow: '最近 {days} 天',
    platforms: {
      aria: '平台',
      anthropic: 'Anthropic',
      openai: 'OpenAI'
    },
    groups: {
      title: '启用分组',
      empty: '没有 Anthropic 分组',
      emptyOpenai: '没有 OpenAI 分组'
    },
    limits: {
      maxRuleSwitches: '单请求最多换号',
      maxRuleSwitchesHint: '一次请求里规则最多触发几次换号（1~10）'
    },
    rules: {
      title: '规则',
      hint: '从上到下，先命中先生效。只作用于原本会直接回给客户的上游 HTTP 4xx；401/403/429 与 5xx 由既有机制换号。',
      add: '添加规则',
      empty: '还没有规则',
      enabled: '启用',
      name: '名称',
      status: '状态码',
      statusHint: '留空 = 任意 4xx；不能填 401/403/429',
      anyStatus: '任意 4xx',
      keywords: '关键字',
      keywordsPlaceholder: '每行一个，如：暂无可用渠道',
      keywordRequired: '每条规则至少要有一个关键字',
      statusOrKeywordRequired: '每条规则要有状态码或至少一个关键字',
      action: '动作',
      actionHint: '「直接返回」只停止本表匹配，错误按原样处理',
      actionFailover: '换号',
      actionReturn: '直接返回',
      hits: '命中',
      recovery: '恢复率',
      moveUp: '上移',
      moveDown: '下移'
    },
    stream: {
      title: '流内报错换号',
      hint: '上游只发过心跳就在流里报错时自动换号，不经过规则表'
    },
    recent: {
      title: '最近事件',
      empty: '暂无事件',
      time: '时间',
      reason: '原因',
      status: '状态码',
      group: '分组',
      accounts: '账号链',
      result: '结果',
      recovered: '已恢复',
      failed: '失败',
      canceled: '客户端断开'
    }
  }
}
