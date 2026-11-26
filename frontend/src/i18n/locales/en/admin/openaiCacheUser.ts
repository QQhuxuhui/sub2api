export default {
  openaiCacheUser: {
    tabs: { accounts: 'By account', users: 'By user' },
    global: {
      title: 'Per-user cache simulation',
      hint: 'When on, users without an override are lifted into the range on the Responses API; new conversations and zero-cache requests are left alone. User overrides win.',
      overrides: '{n} user overrides',
      overridesHint: 'Show only users with an override. Full rollback = turn this off and set each override back to inherit.'
    },
    onlyOverrides: 'Overrides only',
    timezoneHint: 'Time zone: {tz} (matches the acceptance query, independent of the browser)',
    trendFor: 'Cache trend of {name}',
    edit: 'Configure',
    editHint: 'Set this user\'s override (inherit / on / off)',
    table: {
      user: 'User',
      source: 'Effective',
      sourceHint: 'User override > global > the account\'s own simulation',
      reachable: 'Reachable',
      reachableHint: 'Real upper bound from the previous turn\'s input in the request; rows not simulated per user count their actual cache.',
      coverage: 'Coverage {pct}',
      verdict: 'Status'
    },
    source: {
      userOn: 'Override on',
      userOff: 'Override off',
      global: 'Global',
      account: 'By account',
      accountHint: 'Not simulated per user; the account\'s own simulation applies'
    },
    verdict: { ok: 'OK', ceiling_short: 'Ceiling short', strategy_short: 'Short' },
    verdictHint: {
      ok: 'Shown cache rate ≥ the lower bound',
      ceiling_short: 'The real reachable bound is below the lower bound (many new conversations / zero-cache requests)',
      strategy_short: 'Reachable but not lifted to the lower bound; small hours may be limited by 128-token blocks (judged on the current config)'
    },
    verdictEmpty: { off: 'Off', account: 'By account', noInput: 'No input' },
    modal: {
      title: 'Cache simulation of {name}',
      mode: { inherit: 'Inherit', on: 'On', off: 'Off' },
      hint: {
        inherit: 'Remove the override; the global setting applies (the account\'s when global is off).',
        on: 'This user is lifted into the range below regardless of the global switch.',
        off: 'Nothing is lifted for this user on any API; account simulation does not apply either.'
      }
    }
  }
}
