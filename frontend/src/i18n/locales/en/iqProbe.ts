export default {
  iqProbe: {
    nav: 'IQ Probe',
    title: 'IQ Probe',
    description: 'Periodically ask a chosen account + model a set of questions with known answers, and watch the score trend to catch upstreams that swap in a weaker model.',
    howItWorks:
      'Probes go straight to the account upstream (the same path as the account "Test" button), bypass group scheduling, are not billed or logged as usage, and never change account state. Every question gets "On the last line output: 答案：xxx" appended, and only the last "答案：" is judged. Failed requests are left out of the denominator; a round where more than half fail is invalid. Rounds are skipped while the account is disabled, in error or cooling down.',
    minutes: '{n} min',
    tabs: {
      monitors: 'Monitors',
      sets: 'Question sets',
      runs: 'Runs'
    },
    monitors: {
      summary: '{n} monitors, {enabled} enabled',
      add: 'Add monitor',
      needSet: 'Create a question set first',
      empty: 'No monitors yet. Create a question set, then pick accounts and a model from a group.',
      runNow: 'Run now',
      runStarted: 'Started; the list refreshes when it finishes',
      confirmDelete: 'Delete monitor "{name}" and all its runs?'
    },
    columns: {
      account: 'Account',
      model: 'Model',
      set: 'Set',
      score: 'Latest score',
      trend: 'Last 24h',
      lastRun: 'Last run',
      interval: 'Interval',
      alert: 'Alert',
      enabled: 'Enabled'
    },
    alertState: {
      off: 'Off',
      ok: 'OK',
      low: 'Low score',
      invalid: 'Invalid'
    },
    status: {
      never: 'Never run',
      running: 'Running',
      scored: 'Scored',
      invalid: 'Invalid',
      skipped: 'Skipped'
    },
    actions: {
      saved: 'Saved',
      deleted: 'Deleted'
    },
    monitorDialog: {
      createTitle: 'Add monitor',
      editTitle: 'Edit monitor',
      account: 'Account',
      group: 'Group',
      pickGroup: 'Pick a group',
      accounts: 'Accounts ({n} selected)',
      selectAll: 'Select all',
      selectNone: 'Select none',
      noAccounts: 'No accounts in this group',
      model: 'Model',
      modelPlaceholder: 'e.g. gpt-5.5',
      modelHint: 'The model name clients request; it goes through the account model mapping. Suggestions come from the first selected account.',
      set: 'Question set',
      pickSet: 'Pick a set',
      interval: 'Interval (minutes)',
      intervalHint: 'At least 10 minutes; runs drift by ±10%',
      effort: 'Reasoning effort',
      effortDefault: 'Not sent (upstream default)',
      effortHint: 'Leave empty to measure what clients get by default',
      maxTokens: 'Max output tokens',
      maxTokensHint: '0 = path default; ignored for Codex OAuth accounts',
      enabled: 'Enabled',
      alert: 'Alerts',
      alertHint: 'Mailed to the alert recipients under Ops → Email notifications. Each state alerts once; a recovery mail is sent when the score is back above the threshold.',
      threshold: 'Score threshold (%)',
      lowRounds: 'Consecutive low rounds',
      invalidRounds: 'Consecutive invalid rounds',
      created: 'Created {created} monitors; {skipped} accounts already had the same monitor'
    },
    sets: {
      title: 'Question sets',
      add: 'New',
      empty: 'No question sets yet',
      counts: '{questions} questions ({enabled} enabled) · {monitors} monitors',
      enabledCount: '{n} enabled',
      import: 'Import JSON',
      importSample: 'Import sample set',
      export: 'Export JSON',
      noDescription: 'No description',
      inUse: 'Monitors use this set; delete or change them first',
      createTitle: 'New question set',
      editTitle: 'Edit question set',
      name: 'Name',
      description: 'Description',
      confirmDelete: 'Delete set "{name}" and all its questions?',
      importInvalid: 'The file is not valid JSON',
      imported: 'Imported "{name}" with {n} questions'
    },
    questions: {
      add: 'Add question',
      empty: 'This set has no questions',
      title: 'Title',
      prompt: 'Prompt',
      promptHint: 'Sent as the user message. The "答案：xxx" output instruction is appended automatically.',
      system: 'System prompt (optional)',
      expected: 'Expected answer',
      expectedHint: "Separate accepted alternatives with {'||'}, e.g. 42{'||'}forty-two",
      judge: 'Judge',
      weight: 'Weight',
      tolerance: 'Tolerance',
      sortOrder: 'Order',
      enabled: 'Enabled',
      note: 'Note',
      notePlaceholder: 'Source, what it separates, etc.',
      createTitle: 'Add question',
      editTitle: 'Edit question',
      confirmDelete: 'Delete question "{name}"? Past runs keep their copy.',
      hint: 'Only the line after the last "答案：" is judged. Exact ignores case, spacing and surrounding quotes; choice reads the leading option letters (order-free for multi-select); numeric takes the first number and compares within the tolerance.'
    },
    judge: {
      exact: 'Exact',
      choice: 'Choice letters',
      numeric: 'Numeric'
    },
    verdict: {
      correct: 'Correct',
      wrong: 'Wrong',
      format_error: 'Format error',
      error: 'Request failed',
      unsupported: 'Unsupported path'
    },
    detail: {
      title: '{name} / {model}',
      trend: 'Last {n} rounds',
      noRuns: 'No runs yet',
      round: 'Round',
      manual: 'manual',
      cost: 'Est. cost',
      legend: '✓ correct  ✗ wrong  ? format error  ! request failed  – unsupported. Click a cell for the raw reply. Cost is estimated at catalog prices and never charged.',
      expected: 'Expected',
      extracted: 'Extracted',
      upstreamModel: 'Sent upstream',
      responseModel: 'Upstream reported',
      latency: 'Latency',
      firstToken: 'first token',
      tokens: 'Input / output tokens',
      reasoning: 'reasoning',
      emptyReply: '(empty reply)'
    },
    runs: {
      allStatus: 'All statuses',
      allMonitors: 'All monitors',
      time: 'Time',
      breakdown: 'Right / wrong / format / failed',
      breakdownValue: '{c} / {w} / {f} / {e}',
      message: 'Note',
      empty: 'No runs',
      clickHint: 'Click to see every answer of this round'
    },
    runDialog: {
      title: 'Round result: {name} / {model}',
      reply: 'Raw reply',
      noAnswers: 'No answers for this round (skipped, details pruned after 30 days, or still running)'
    },
    errors: {
      IQ_PROBE_SET_NOT_FOUND: 'Question set not found',
      IQ_PROBE_QUESTION_NOT_FOUND: 'Question not found',
      IQ_PROBE_MONITOR_NOT_FOUND: 'Monitor not found',
      IQ_PROBE_SET_IN_USE: 'Monitors use this set; delete or change them first',
      IQ_PROBE_MONITOR_DUPLICATE: 'This account already has a monitor for the same model and set',
      IQ_PROBE_MONITOR_BUSY: 'This monitor is already running'
    }
  }
}
