/**
 * User-facing IQ showcase: SVG cards that admin-configured probes drew
 * through the real gateway. Both endpoints answer 404 IQ_SHOWCASE_DISABLED
 * while the feature is switched off.
 */
import { apiClient } from './client'

export interface IQShowcaseCardMeta {
  id: number
  model: string
  created_at: string
}

export interface IQShowcaseUserItem {
  id: number
  title: string
  cards: IQShowcaseCardMeta[]
}

export interface IQShowcaseUserGroup {
  group_id: number
  group_name: string
  items: IQShowcaseUserItem[]
}

export interface IQShowcaseView {
  groups: IQShowcaseUserGroup[]
}

export interface IQShowcaseCardSVG {
  id: number
  svg: string
}

/** The server accepts at most this many ids per SVG request. */
export const IQ_SHOWCASE_SVG_BATCH = 24

export const iqShowcaseAPI = {
  view() {
    return apiClient.get<IQShowcaseView>('/iq-showcase')
  },
  cards(ids: number[]) {
    return apiClient.get<{ cards: IQShowcaseCardSVG[] }>('/iq-showcase/cards', { params: { ids: ids.join(',') } })
  },
}

/**
 * Turns an SVG into an <img> source. Rendering through an image keeps the
 * model's markup from running scripts or loading anything: never v-html it.
 */
export function iqShowcaseSVGDataURL(svg: string): string {
  const bytes = new TextEncoder().encode(svg)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return `data:image/svg+xml;base64,${btoa(binary)}`
}
