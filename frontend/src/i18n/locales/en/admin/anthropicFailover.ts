export default {
  anthropicFailover: {
    title: 'Anthropic Failover Rules',
    description: 'Switch accounts by rule when an upstream fails, without penalising the account.',
    saved: 'Saved',
    failed: 'Operation failed',
    statsWindow: 'Last {days} days',
    groups: {
      title: 'Enabled groups',
      empty: 'No Anthropic groups'
    },
    limits: {
      maxRuleSwitches: 'Max switches per request',
      maxRuleSwitchesHint: 'How many account switches rules may trigger in one request (1–10)'
    },
    rules: {
      title: 'Rules',
      hint: 'Top to bottom, first match wins. Only upstream HTTP 4xx that would be returned to the customer; 401/403/429 and 5xx already switch accounts.',
      add: 'Add rule',
      empty: 'No rules yet',
      enabled: 'On',
      name: 'Name',
      status: 'Status',
      statusHint: 'Empty = any 4xx; 401/403/429 are not allowed',
      anyStatus: 'Any 4xx',
      keywords: 'Keywords',
      keywordsPlaceholder: 'One per line, e.g. no available channel',
      keywordRequired: 'Every rule needs at least one keyword',
      action: 'Action',
      actionHint: '"Return" only stops matching in this table; the error is handled as before',
      actionFailover: 'Switch account',
      actionReturn: 'Return',
      hits: 'Hits',
      recovery: 'Recovery',
      moveUp: 'Move up',
      moveDown: 'Move down'
    },
    stream: {
      title: 'In-stream error switches',
      hint: 'When an upstream fails in-stream after sending only heartbeats, the account is switched automatically, without the rule table'
    },
    recent: {
      title: 'Recent events',
      empty: 'No events',
      time: 'Time',
      reason: 'Reason',
      status: 'Status',
      group: 'Group',
      accounts: 'Accounts',
      result: 'Result',
      recovered: 'Recovered',
      failed: 'Failed',
      canceled: 'Client left'
    }
  }
}
