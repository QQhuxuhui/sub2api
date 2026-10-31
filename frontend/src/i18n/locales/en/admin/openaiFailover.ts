export default {
  openaiFailover: {
    title: 'Failover Rules',
    description: 'Switch accounts on upstream errors before anything reaches the client',
    saved: 'Saved',
    failed: 'Operation failed',
    statsWindow: 'Hits and success rates cover the last 7 days, per request',
    groups: {
      title: 'Enabled groups',
      hint: 'Applies to /v1/responses HTTP requests of the selected groups: rule failover, failover on slot wait timeout, and rewriting plaintext encrypted_content.',
      empty: 'No OpenAI groups'
    },
    limits: {
      slotWait: 'Slot wait limit (ms)',
      slotWaitHint: 'Move on to the next account when no slot frees up in this time.',
      maxSlotSwitches: 'Slot switch limit (accounts)',
      maxRuleSwitches: 'Rule switch limit (accounts)'
    },
    rules: {
      title: 'Rules',
      hint: 'Matched top to bottom; the first match decides. Rules only handle errors the platform would return as is: 401, 429, 5xx, context window and cyber verdicts are unaffected. Rules without a status also match in-stream response.failed events; rules with a status match HTTP errors only. Rule switches skip same-account retries and never penalize the account.',
      add: 'Add rule',
      enabled: 'On',
      name: 'Name',
      status: 'Status',
      anyStatus: 'Any',
      keywords: 'Keywords (any)',
      keywordsPlaceholder: 'One per line, case insensitive; empty matches on status alone',
      action: 'Action',
      actionFailover: 'Switch',
      actionReturn: 'Return',
      hits: 'Hits',
      recovery: 'Recovered',
      moveUp: 'Move up',
      moveDown: 'Move down'
    },
    summary: {
      slotSwitched: 'Requests switched on slot timeout',
      recovered: 'Succeeded',
      rewritten: 'Requests with plaintext rewritten',
      rewrittenItems: 'Items rewritten'
    },
    accounts: {
      title: 'Accounts without encrypted arguments',
      hint: 'Requests declaring encrypted:true tools are not scheduled to marked accounts; other requests are unaffected. The mark applies site-wide, regardless of the group switch. A check calls the upstream three times and only shows the result; it never changes the mark.',
      search: 'Search accounts',
      name: 'Account',
      mark: 'Unsupported',
      model: 'Check model',
      check: 'Check',
      paused: 'not scheduled',
      runCheck: 'Check',
      checking: 'Checking',
      noCheck: 'API-key accounts only',
      counts: 'cipher {encrypted} / plain {plaintext} / failed {failed}',
      verdict: {
        supported: 'Supported',
        unsupported: 'Unsupported',
        mixed: 'Mixed',
        unknown: 'Inconclusive'
      }
    },
    recent: {
      title: 'Recent switches',
      time: 'Time',
      reason: 'Reason',
      slot: 'Slot wait timeout',
      group: 'Group',
      apiKey: 'API key',
      accounts: 'Accounts tried',
      result: 'Result',
      recovered: 'Succeeded',
      failed: 'Failed',
      empty: 'Nothing yet'
    }
  }
}
