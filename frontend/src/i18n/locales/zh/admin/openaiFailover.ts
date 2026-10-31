export default {
  openaiFailover: {
    title: '换号规则',
    description: '让上游报错先换号再决定是否返回下游',
    saved: '已保存',
    failed: '操作失败',
    statsWindow: '命中数和成功率为最近 7 天，按请求计',
    groups: {
      title: '启用分组',
      hint: '只对勾选分组的 /v1/responses HTTP 请求生效，同时开启规则换号、等槽超时换号和明文 encrypted_content 改写。',
      empty: '没有 OpenAI 分组'
    },
    limits: {
      slotWait: '等槽上限（毫秒）',
      slotWaitHint: '等不到账号槽位时，超过这个时间就换下一个账号。',
      maxSlotSwitches: '等槽换号上限（个账号）',
      maxRuleSwitches: '规则换号上限（个账号）'
    },
    rules: {
      title: '规则表',
      hint: '从上到下匹配，第一条命中的生效。只处理平台本来要直接返回的错误，401、429、5xx 和上下文超限、cyber 的判定不受影响。不填状态码的规则也匹配流内 response.failed，填了状态码的只匹配 HTTP 错误。规则换号不做同号重试，也不惩罚账号。',
      add: '添加规则',
      enabled: '启用',
      name: '名称',
      status: '状态码',
      anyStatus: '任意',
      keywords: '关键字（任一命中）',
      keywordsPlaceholder: '一行一个，不区分大小写；留空只看状态码',
      action: '动作',
      actionFailover: '换号',
      actionReturn: '返回',
      hits: '命中',
      recovery: '换号成功率',
      moveUp: '上移',
      moveDown: '下移'
    },
    summary: {
      slotSwitched: '等槽超时换号的请求',
      recovered: '最终成功',
      rewritten: '改写过明文的请求',
      rewrittenItems: '改写项数'
    },
    accounts: {
      title: '不支持加密参数的账号',
      hint: '打标记后，声明了 encrypted:true 工具的请求不会调度到这个账号，其他请求照常走。标记全站生效，不受分组开关影响。检测会真实调用上游 3 次，只显示结果，不会自动改标记。',
      search: '搜索账号',
      name: '账号',
      mark: '不支持',
      model: '检测模型',
      check: '检测',
      paused: '未调度',
      runCheck: '检测',
      checking: '检测中',
      noCheck: '仅 API Key 账号可检测',
      counts: '密文 {encrypted} / 明文 {plaintext} / 失败 {failed}',
      verdict: {
        supported: '支持',
        unsupported: '不支持',
        mixed: '时好时坏',
        unknown: '未测出'
      }
    },
    recent: {
      title: '最近换号记录',
      time: '时间',
      reason: '原因',
      slot: '等槽超时',
      group: '分组',
      apiKey: '令牌',
      accounts: '换过的账号',
      result: '结果',
      recovered: '成功',
      failed: '失败',
      empty: '暂无记录'
    }
  }
}
