import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref, type Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useHomeEffects, type HomeEffectRefs } from '../useHomeEffects'

const fx = vi.hoisted(() => ({
  lenisCreated: vi.fn(),
  lenisDestroyed: vi.fn(),
  rendererCreated: vi.fn(),
  rendererDisposed: vi.fn()
}))

vi.mock('lenis', () => ({
  default: class MockLenis {
    constructor() {
      fx.lenisCreated()
      document.documentElement.classList.add('lenis')
    }

    on() {}
    raf() {}

    destroy() {
      fx.lenisDestroyed()
      document.documentElement.classList.remove('lenis')
    }
  }
}))

vi.mock('gsap', () => {
  const tween = () => ({
    kill: vi.fn(),
    scrollTrigger: { kill: vi.fn() }
  })

  return {
    gsap: {
      from: vi.fn(tween),
      fromTo: vi.fn(tween),
      quickTo: vi.fn(() => vi.fn()),
      registerPlugin: vi.fn(),
      set: vi.fn()
    }
  }
})

vi.mock('gsap/ScrollTrigger', () => ({
  ScrollTrigger: {
    refresh: vi.fn(),
    update: vi.fn()
  }
}))

vi.mock('three', () => {
  class MockRenderer {
    private readonly canvas: HTMLCanvasElement

    constructor({ canvas }: { canvas: HTMLCanvasElement }) {
      this.canvas = canvas
      fx.rendererCreated(canvas)
    }

    setSize(width: number, height: number) {
      this.canvas.width = width
      this.canvas.height = height
    }

    setPixelRatio() {}
    render() {}

    dispose() {
      fx.rendererDisposed(this.canvas)
    }
  }

  class MockBufferGeometry {
    attributes: Record<string, { needsUpdate: boolean }> = {}

    setAttribute(name: string, attribute: { needsUpdate: boolean }) {
      this.attributes[name] = attribute
    }

    setDrawRange() {}
    dispose() {}
  }

  class MockBufferAttribute {
    needsUpdate = false

    constructor(
      public readonly array: Float32Array,
      public readonly itemSize: number
    ) {}
  }

  class MockMaterial {
    color = { set: vi.fn() }
    opacity = 1
    dispose() {}
  }

  class MockPerspectiveCamera {
    aspect = 1
    position = { x: 0, y: 0, z: 0 }

    updateProjectionMatrix() {}
    lookAt() {}
  }

  class MockOrthographicCamera {
    right = 1
    top = 1

    updateProjectionMatrix() {}
  }

  return {
    WebGLRenderer: MockRenderer,
    Scene: class {
      add() {}
    },
    PerspectiveCamera: MockPerspectiveCamera,
    OrthographicCamera: MockOrthographicCamera,
    BufferGeometry: MockBufferGeometry,
    BufferAttribute: MockBufferAttribute,
    PointsMaterial: MockMaterial,
    LineBasicMaterial: MockMaterial,
    Points: class {},
    LineSegments: class {},
    CanvasTexture: class {
      dispose() {}
    }
  }
})

const OriginalIntersectionObserver = globalThis.IntersectionObserver

function createEffectRefs(): HomeEffectRefs {
  return {
    heroCanvas: ref(null),
    routeCanvas: ref(null),
    routeSrc: ref(null),
    routeDst: ref(null),
    terminalWrap: ref(null),
    terminal: ref(null),
    chips: ref(null),
    painGrid: ref(null),
    stepsSection: ref(null),
    stepLine: ref(null),
    ctaCard: ref(null)
  }
}

function renderDefaultHome(refs: HomeEffectRefs) {
  return h('main', [
    h('canvas', { ref: refs.heroCanvas }),
    h('div', { ref: refs.terminalWrap }, [
      h('div', { ref: refs.terminal }, [h('div', { class: 'terminal-glare' })])
    ]),
    h('div', { ref: refs.chips }, [h('div', { class: 'chip-magnetic' })]),
    h('div', { ref: refs.painGrid }, [h('div'), h('div')]),
    h('div', [
      h('canvas', { ref: refs.routeCanvas }),
      h('div', { ref: refs.routeSrc }),
      h('div', { ref: refs.routeDst })
    ]),
    h('section', { ref: refs.stepsSection }, [
      h('svg', [h('path', { ref: refs.stepLine })]),
      h('div', { class: 'step-dot' })
    ]),
    h('div', { ref: refs.ctaCard })
  ])
}

function mountHarness(enabled: Ref<boolean>) {
  const refs = createEffectRefs()
  const onError = vi.fn()
  const Harness = defineComponent({
    setup() {
      useHomeEffects(refs, ref(false), enabled)

      return () =>
        enabled.value ? renderDefaultHome(refs) : h('main', { id: 'custom-home' }, 'Custom home')
    }
  })

  const wrapper = mount(Harness, {
    attachTo: document.body,
    global: { config: { errorHandler: onError } }
  })

  return { wrapper, onError }
}

async function settleEffects() {
  await nextTick()
  await flushPromises()
  await nextTick()
}

describe('useHomeEffects', () => {
  beforeEach(() => {
    fx.lenisCreated.mockClear()
    fx.lenisDestroyed.mockClear()
    fx.rendererCreated.mockClear()
    fx.rendererDisposed.mockClear()
    document.documentElement.classList.remove('lenis')
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({ matches: false }))
    })
    Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
      configurable: true,
      value: vi.fn(() => null)
    })
    vi.stubGlobal('requestAnimationFrame', vi.fn(() => 1))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    vi.stubGlobal('IntersectionObserver', OriginalIntersectionObserver)
  })

  afterEach(() => {
    document.body.innerHTML = ''
    document.documentElement.classList.remove('lenis')
    vi.unstubAllGlobals()
  })

  it('starts only for the default home branch and stops when that branch is removed', async () => {
    const enabled = ref(false)
    const { wrapper, onError } = mountHarness(enabled)

    await settleEffects()
    expect(fx.lenisCreated).not.toHaveBeenCalled()
    expect(fx.rendererCreated).not.toHaveBeenCalled()

    enabled.value = true
    await settleEffects()
    expect(fx.lenisCreated).toHaveBeenCalledTimes(1)
    expect(fx.rendererCreated).toHaveBeenCalledTimes(2)
    expect(document.documentElement.classList.contains('lenis')).toBe(true)

    enabled.value = false
    await settleEffects()
    expect(fx.lenisDestroyed).toHaveBeenCalledTimes(1)
    expect(fx.rendererDisposed).toHaveBeenCalledTimes(2)
    expect(document.documentElement.classList.contains('lenis')).toBe(false)
    expect(onError).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('keeps both canvas effects running when IntersectionObserver is unavailable', async () => {
    Reflect.deleteProperty(globalThis, 'IntersectionObserver')
    const { wrapper, onError } = mountHarness(ref(true))

    await settleEffects()
    expect(onError).not.toHaveBeenCalled()
    expect(fx.rendererCreated).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })
})
