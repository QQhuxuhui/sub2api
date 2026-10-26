export default {
  iqProbe: {
    nav: '智力探测',
    title: '智力探测',
    description: '按固定间隔给指定账号 + 模型做一套有标准答案的题，用分数走势发现上游偷换小模型或降智。',
    howItWorks:
      '探测直连账号上游（与账号「测试」同一通道），不经过分组调度、不扣费、不写使用记录，也不会改账号状态。每道题末尾会自动追加「最后单独一行输出：答案：xxx」，只按最后一个「答案：」判分；请求失败的题不计入分母，一轮超过一半失败记为无效。账号停用、出错或冷却中时本轮跳过。',
    minutes: '{n} 分钟',
    tabs: {
      monitors: '监控列表',
      sets: '题组与题目',
      runs: '运行记录'
    },
    monitors: {
      summary: '共 {n} 条监控，{enabled} 条启用',
      add: '添加监控',
      needSet: '先在「题组与题目」里建一个题组',
      empty: '还没有监控。先建题组，再从分组里挑账号和模型加入监控。',
      runNow: '立即运行',
      runStarted: '已开始运行，完成后自动刷新',
      confirmDelete: '删除监控「{name}」及其全部运行记录？'
    },
    columns: {
      account: '账号',
      model: '模型',
      set: '题组',
      score: '最新得分',
      trend: '近 24 小时',
      lastRun: '上次运行',
      interval: '间隔',
      alert: '告警',
      enabled: '启用'
    },
    alertState: {
      off: '未开启',
      ok: '正常',
      low: '低分告警中',
      invalid: '无效告警中'
    },
    status: {
      never: '未运行',
      running: '运行中',
      scored: '已评分',
      invalid: '无效',
      skipped: '跳过'
    },
    actions: {
      saved: '已保存',
      deleted: '已删除'
    },
    monitorDialog: {
      createTitle: '添加监控',
      editTitle: '编辑监控',
      account: '账号',
      group: '分组',
      pickGroup: '选择分组',
      accounts: '账号（已选 {n} 个）',
      selectAll: '全选',
      selectNone: '全不选',
      noAccounts: '这个分组下没有账号',
      model: '模型',
      modelPlaceholder: '如 gpt-5.5',
      modelHint: '填下游请求用的模型名，发出前按账号的模型映射转换。候选来自第一个选中账号的可用模型，也可手填。',
      set: '题组',
      pickSet: '选择题组',
      interval: '间隔（分钟）',
      intervalHint: '最少 10 分钟，实际执行有 ±10% 抖动',
      effort: '推理强度',
      effortDefault: '不传（上游默认）',
      effortHint: '留空测的是下游客户默认拿到的水平',
      maxTokens: '最大输出 token',
      maxTokensHint: '0 = 按通道默认；Codex OAuth 账号不支持该参数会忽略',
      enabled: '启用',
      alert: '告警',
      alertHint: '发到「运维监控 → 邮件通知」里配置的告警收件人；同一状态只发一次，分数回到阈值以上时发恢复通知。',
      threshold: '得分阈值（%）',
      lowRounds: '连续低分轮数',
      invalidRounds: '连续无效轮数',
      created: '已创建 {created} 条监控，{skipped} 个账号已存在相同监控被跳过'
    },
    sets: {
      title: '题组',
      add: '新建',
      empty: '还没有题组',
      counts: '{questions} 题（启用 {enabled}）· {monitors} 条监控',
      enabledCount: '启用 {n} 题',
      import: '导入 JSON',
      importSample: '导入示例题组',
      export: '导出 JSON',
      noDescription: '没有描述',
      inUse: '有监控在用这个题组，先删除或改掉这些监控',
      createTitle: '新建题组',
      editTitle: '编辑题组',
      name: '名称',
      description: '描述',
      confirmDelete: '删除题组「{name}」及其全部题目？',
      importInvalid: '文件不是有效的 JSON',
      imported: '已导入题组「{name}」，共 {n} 题'
    },
    questions: {
      add: '添加题目',
      empty: '这个题组还没有题目',
      title: '标题',
      prompt: '题面',
      promptHint: '作为 user 消息发出；系统会自动在末尾追加输出「答案：xxx」的要求，不用自己写。',
      system: 'System 提示（可选）',
      expected: '标准答案',
      expectedHint: '多个可接受答案用 || 分隔，如 42||四十二',
      judge: '判分方式',
      weight: '权重',
      tolerance: '允许误差',
      sortOrder: '排序',
      enabled: '启用',
      note: '备注',
      notePlaceholder: '出处、区分度说明等，如「sol 能对、luna 常错」',
      createTitle: '添加题目',
      editTitle: '编辑题目',
      confirmDelete: '删除题目「{name}」？已有运行记录里的这道题仍会保留。',
      hint: '判分只看模型最后一个「答案：」后面那一行。精确匹配忽略大小写、空白和首尾引号；选择题只取开头的选项字母（多选不计顺序）；数值题取第一个数字并按允许误差比较。'
    },
    judge: {
      exact: '精确匹配',
      choice: '选择题字母',
      numeric: '数值'
    },
    verdict: {
      correct: '答对',
      wrong: '答错',
      format_error: '格式不合规',
      error: '请求失败',
      unsupported: '通道不支持'
    },
    detail: {
      title: '{name} / {model}',
      trend: '最近 {n} 轮得分',
      noRuns: '还没有运行记录',
      round: '轮次',
      manual: '手动',
      cost: '估算成本',
      legend: '✓ 答对　✗ 答错　? 格式不合规　! 请求失败　– 通道不支持；点格子看原始回复。成本按目录价估算，不扣任何余额。',
      expected: '标准答案',
      extracted: '提取到',
      upstreamModel: '实际发给上游',
      responseModel: '上游自报',
      latency: '耗时',
      firstToken: '首字',
      tokens: '输入 / 输出 token',
      reasoning: '推理',
      emptyReply: '（无回复内容）'
    },
    runs: {
      allStatus: '全部状态',
      allMonitors: '全部监控',
      time: '时间',
      breakdown: '对 / 错 / 格式 / 失败',
      breakdownValue: '{c} / {w} / {f} / {e}',
      message: '说明',
      empty: '没有运行记录'
    },
    errors: {
      IQ_PROBE_SET_NOT_FOUND: '题组不存在',
      IQ_PROBE_QUESTION_NOT_FOUND: '题目不存在',
      IQ_PROBE_MONITOR_NOT_FOUND: '监控不存在',
      IQ_PROBE_SET_IN_USE: '有监控在用这个题组，先删除或改掉这些监控',
      IQ_PROBE_MONITOR_DUPLICATE: '这个账号已经有相同模型和题组的监控',
      IQ_PROBE_MONITOR_BUSY: '这条监控正在运行，稍后再试'
    }
  }
}
