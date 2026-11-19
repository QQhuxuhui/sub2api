/** Model plaza board (features/model-plaza-board) strings. */
export default {
  plazaBoard: {
    searchPlaceholder: 'Search models, e.g. gpt-6.1, opus',
    view: { label: 'View', card: 'Cards', list: 'List' },
    summary: { label: 'Filter by status', all: 'All models' },
    state: { healthy: 'Healthy', warning: 'Unstable', critical: 'Down', unknown: 'No calls' },
    filters: {
      group: 'Group',
      allGroups: 'All groups',
      findGroup: 'Find a group',
      models: '{count} models',
      exclusive: 'Exclusive group',
      vendor: 'Vendor',
      billing: 'Billing',
      all: 'All',
      token: 'Per token',
      request: 'Per request',
      otherVendor: 'Other'
    },
    sort: {
      label: 'Sort',
      rank: 'Most used',
      name: 'Name',
      price: 'Price: low to high',
      stable: 'Most stable',
      fast: 'Fastest'
    },
    count: '{models} models · {lines} routes',
    billing: { token: 'Per token', request: 'Per request', image: 'Per image', video: 'Video' },
    recommended: 'Recommended route',
    price: {
      input: 'Input',
      output: 'Output',
      cacheRead: 'Cache read',
      perMillion: '/ 1M',
      perRequest: '/ request',
      perImage: '/ image',
      single: 'Per call',
      from: 'from',
      unset: 'Not configured',
      official: 'Official {input} / {output}'
    },
    group: 'Group',
    availability: 'Uptime',
    ttft: 'TTFT',
    details: 'Details',
    copy: 'Copy model name',
    strip: 'Latest 24 hours with calls',
    sampleTitle: '{time} · success rate {rate}',
    idle: 'No calls in 7 days',
    hoursAgo: '{n}h ago',
    daysAgo: '{n}d ago',
    list: { model: 'Model', status: 'Status', strip: 'Recent status' },
    drawer: {
      lines: '{count} routes',
      close: 'Close',
      rate: 'Rate ×{rate}',
      cheapest: 'Cheapest',
      steadiest: 'Most stable',
      fastest: 'Fastest',
      fullPricing: 'Full pricing details'
    },
    emptyFiltered: 'No models match. Clear the search or change the filters.'
  }
}
