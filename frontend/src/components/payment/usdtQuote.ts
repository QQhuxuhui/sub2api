/**
 * USDT-quoted payments: an Epusdt instance priced in USD is entered and shown
 * in whole USDT, and the balance follows what actually arrives on chain.
 */

export const USDT_QUOTE_UNIT = 'USDT'

const NETWORK_LABELS: Record<string, string> = {
  binance: 'BSC',
  bsc: 'BSC',
  tron: 'TRC20',
  polygon: 'Polygon',
  ethereum: 'ERC20',
}

export function usdtNetworkLabel(network?: string | null): string {
  const key = String(network || '').trim().toLowerCase()
  return NETWORK_LABELS[key] || key.toUpperCase()
}

/** A USDT order is USDT-quoted when its gateway currency is USD. */
export function isUsdtQuotedOrder(paymentType?: string | null, currency?: string | null): boolean {
  return String(paymentType || '').trim().toLowerCase() === 'usdt'
    && String(currency || '').trim().toUpperCase() === 'USD'
}

export function formatUsdtAmount(value: number): string {
  const n = Number.isFinite(value) ? value : 0
  const text = Number.isInteger(n) ? String(n) : n.toFixed(2).replace(/0+$/, '').replace(/\.$/, '')
  return `${text} ${USDT_QUOTE_UNIT}`
}
