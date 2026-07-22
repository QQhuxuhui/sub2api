/**
 * Landing page 3D / interaction effects (Three.js + GSAP + Lenis).
 *
 * All effects are additive to the existing design: they draw on transparent
 * canvases or drive transforms only, so the page renders identically when
 * this composable bails out (reduced motion, no WebGL, test environment).
 * Heavy libraries are imported dynamically on mount to keep them out of the
 * initial route chunk.
 */
import { nextTick, onMounted, onBeforeUnmount, watch, type Ref } from 'vue'

export interface HomeEffectRefs {
  heroCanvas: Ref<HTMLCanvasElement | null>
  routeCanvas: Ref<HTMLCanvasElement | null>
  routeSrc: Ref<HTMLElement | null>
  routeDst: Ref<HTMLElement | null>
  terminalWrap: Ref<HTMLElement | null>
  terminal: Ref<HTMLElement | null>
  chips: Ref<HTMLElement | null>
  painGrid: Ref<HTMLElement | null>
  stepsSection: Ref<HTMLElement | null>
  stepLine: Ref<SVGPathElement | null>
  ctaCard: Ref<HTMLElement | null>
}

type Disposer = () => void

export function useHomeEffects(
  refs: HomeEffectRefs,
  isDark: Ref<boolean>,
  enabled: Ref<boolean>
) {
  const disposers: Disposer[] = []
  const themeTuners: Array<() => void> = []
  let mounted = false
  let running = false
  let generation = 0

  watch(isDark, () => themeTuners.forEach((fn) => fn()))
  watch(
    enabled,
    (isEnabled) => {
      if (!mounted) return
      if (isEnabled) {
        void startEffects()
      } else {
        stopEffects()
      }
    },
    { flush: 'post' }
  )

  onMounted(() => {
    mounted = true
    if (enabled.value) void startEffects()
  })

  onBeforeUnmount(() => {
    mounted = false
    stopEffects()
  })

  async function startEffects() {
    if (running || !mounted || !enabled.value) return
    const currentGeneration = ++generation

    // When a v-if branch becomes active, wait for all template refs to settle.
    await nextTick()
    if (currentGeneration !== generation || !mounted || !enabled.value) return
    if (typeof window === 'undefined') return
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduce) return

    let THREE: typeof import('three')
    let gsap: typeof import('gsap').gsap
    let ScrollTrigger: typeof import('gsap/ScrollTrigger').ScrollTrigger
    let LenisCtor: typeof import('lenis').default
    try {
      const [threeMod, gsapMod, stMod, lenisMod] = await Promise.all([
        import('three'),
        import('gsap'),
        import('gsap/ScrollTrigger'),
        import('lenis')
      ])
      THREE = threeMod
      gsap = gsapMod.gsap
      ScrollTrigger = stMod.ScrollTrigger
      LenisCtor = lenisMod.default
    } catch {
      return // effects are optional; the page works without them
    }
    if (currentGeneration !== generation || !mounted || !enabled.value) return
    running = true
    gsap.registerPlugin(ScrollTrigger)

    /* ---------- Lenis smooth scrolling ---------- */
    try {
      const lenis = new LenisCtor({ lerp: 0.11 })
      lenis.on('scroll', ScrollTrigger.update)
      let rafId = 0
      const raf = (time: number) => {
        lenis.raf(time)
        rafId = requestAnimationFrame(raf)
      }
      rafId = requestAnimationFrame(raf)
      disposers.push(() => {
        cancelAnimationFrame(rafId)
        lenis.destroy()
      })
    } catch {
      /* smooth scroll is optional */
    }

    /* ---------- Hero particle field ---------- */
    initHeroField(THREE)

    /* ---------- Terminal 3D tilt + glare ---------- */
    initTerminalTilt(gsap)

    /* ---------- Magnetic provider chips ---------- */
    initMagneticChips(gsap)

    /* ---------- Pain point cards stagger ---------- */
    initPainStagger(gsap)

    /* ---------- Routing particle flow ---------- */
    initRouteFlow(THREE)

    /* ---------- Steps line draw ---------- */
    initStepLine(gsap, ScrollTrigger)

    /* ---------- CTA pointer spotlight ---------- */
    initCtaSpotlight()
  }

  function stopEffects() {
    generation++
    running = false
    for (const dispose of disposers.splice(0).reverse()) {
      try {
        dispose()
      } catch {
        // Effects are optional, so one failed cleanup must not block the rest.
      }
    }
    themeTuners.length = 0
  }

  function initHeroField(THREE: typeof import('three')) {
    const canvas = refs.heroCanvas.value
    if (!canvas) return
    let renderer: import('three').WebGLRenderer
    try {
      renderer = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: true })
    } catch {
      canvas.style.display = 'none'
      return
    }

    const scene = new THREE.Scene()
    const camera = new THREE.PerspectiveCamera(60, 2, 1, 300)
    camera.position.z = 90

    const COUNT = 150
    const SPREAD_X = 170
    const SPREAD_Y = 90
    const SPREAD_Z = 50
    const positions = new Float32Array(COUNT * 3)
    const velocities: Array<{ x: number; y: number; z: number }> = []
    for (let i = 0; i < COUNT; i++) {
      positions[i * 3] = (Math.random() - 0.5) * SPREAD_X
      positions[i * 3 + 1] = (Math.random() - 0.5) * SPREAD_Y
      positions[i * 3 + 2] = (Math.random() - 0.5) * SPREAD_Z
      velocities.push({
        x: (Math.random() - 0.5) * 0.035,
        y: (Math.random() - 0.5) * 0.028,
        z: (Math.random() - 0.5) * 0.02
      })
    }
    const pointGeo = new THREE.BufferGeometry()
    pointGeo.setAttribute('position', new THREE.BufferAttribute(positions, 3))
    const pointMat = new THREE.PointsMaterial({ size: 1.6, transparent: true, depthWrite: false })
    scene.add(new THREE.Points(pointGeo, pointMat))

    const MAX_LINKS = 460
    const LINK_DIST = 24
    const lineGeo = new THREE.BufferGeometry()
    const linePositions = new Float32Array(MAX_LINKS * 6)
    lineGeo.setAttribute('position', new THREE.BufferAttribute(linePositions, 3))
    const lineMat = new THREE.LineBasicMaterial({ transparent: true, depthWrite: false })
    scene.add(new THREE.LineSegments(lineGeo, lineMat))

    const tune = () => {
      if (isDark.value) {
        pointMat.color.set('#2dd4bf')
        pointMat.opacity = 0.75
        lineMat.color.set('#2dd4bf')
        lineMat.opacity = 0.16
      } else {
        pointMat.color.set('#0d9488')
        pointMat.opacity = 0.5
        lineMat.color.set('#0d9488')
        lineMat.opacity = 0.12
      }
    }
    tune()
    themeTuners.push(tune)

    let mouseX = 0
    let mouseY = 0
    const onPointer = (e: PointerEvent) => {
      mouseX = e.clientX / window.innerWidth - 0.5
      mouseY = e.clientY / window.innerHeight - 0.5
    }
    window.addEventListener('pointermove', onPointer, { passive: true })

    const resize = () => {
      const w = canvas.clientWidth || 1
      const h = canvas.clientHeight || 1
      renderer.setSize(w, h, false)
      renderer.setPixelRatio(Math.min(2, window.devicePixelRatio || 1))
      camera.aspect = w / h
      camera.updateProjectionMatrix()
    }
    resize()
    window.addEventListener('resize', resize)

    let visible = true
    let io: IntersectionObserver | null = null
    if (typeof IntersectionObserver === 'function') {
      io = new IntersectionObserver((entries) => {
        visible = entries[0]?.isIntersecting ?? true
      })
      io.observe(canvas)
    }

    let rafId = 0
    const frame = () => {
      rafId = requestAnimationFrame(frame)
      if (!visible) return
      const p = positions
      for (let i = 0; i < COUNT; i++) {
        p[i * 3] += velocities[i].x
        p[i * 3 + 1] += velocities[i].y
        p[i * 3 + 2] += velocities[i].z
        if (Math.abs(p[i * 3]) > SPREAD_X / 2) velocities[i].x *= -1
        if (Math.abs(p[i * 3 + 1]) > SPREAD_Y / 2) velocities[i].y *= -1
        if (Math.abs(p[i * 3 + 2]) > SPREAD_Z / 2) velocities[i].z *= -1
      }
      pointGeo.attributes.position.needsUpdate = true

      let links = 0
      for (let i = 0; i < COUNT && links < MAX_LINKS; i++) {
        for (let j = i + 1; j < COUNT && links < MAX_LINKS; j++) {
          const dx = p[i * 3] - p[j * 3]
          const dy = p[i * 3 + 1] - p[j * 3 + 1]
          const dz = p[i * 3 + 2] - p[j * 3 + 2]
          if (dx * dx + dy * dy + dz * dz < LINK_DIST * LINK_DIST) {
            linePositions.set(
              [p[i * 3], p[i * 3 + 1], p[i * 3 + 2], p[j * 3], p[j * 3 + 1], p[j * 3 + 2]],
              links * 6
            )
            links++
          }
        }
      }
      lineGeo.setDrawRange(0, links * 2)
      lineGeo.attributes.position.needsUpdate = true

      camera.position.x += (mouseX * 10 - camera.position.x) * 0.04
      camera.position.y += (-mouseY * 6 - camera.position.y) * 0.04
      camera.lookAt(0, 0, 0)
      renderer.render(scene, camera)
    }
    frame()

    disposers.push(() => {
      cancelAnimationFrame(rafId)
      window.removeEventListener('pointermove', onPointer)
      window.removeEventListener('resize', resize)
      io?.disconnect()
      pointGeo.dispose()
      pointMat.dispose()
      lineGeo.dispose()
      lineMat.dispose()
      renderer.dispose()
    })
  }

  function initTerminalTilt(gsap: typeof import('gsap').gsap) {
    const wrap = refs.terminalWrap.value
    const el = refs.terminal.value
    if (!wrap || !el) return
    const glare = el.querySelector<HTMLElement>('.terminal-glare')
    const rotX = gsap.quickTo(el, 'rotationX', { duration: 0.5, ease: 'power2.out' })
    const rotY = gsap.quickTo(el, 'rotationY', { duration: 0.5, ease: 'power2.out' })
    const onMove = (e: PointerEvent) => {
      const r = wrap.getBoundingClientRect()
      const px = (e.clientX - r.left) / r.width - 0.5
      const py = (e.clientY - r.top) / r.height - 0.5
      rotY(px * 7)
      rotX(-py * 6)
      if (glare) {
        glare.style.setProperty('--gx', `${(px + 0.5) * 100}%`)
        glare.style.setProperty('--gy', `${(py + 0.5) * 100}%`)
      }
    }
    const onLeave = () => {
      rotX(0)
      rotY(0)
    }
    wrap.addEventListener('pointermove', onMove)
    wrap.addEventListener('pointerleave', onLeave)
    disposers.push(() => {
      wrap.removeEventListener('pointermove', onMove)
      wrap.removeEventListener('pointerleave', onLeave)
      gsap.set(el, { rotationX: 0, rotationY: 0 })
    })
  }

  function initMagneticChips(gsap: typeof import('gsap').gsap) {
    const container = refs.chips.value
    if (!container) return
    container.querySelectorAll<HTMLElement>('.chip-magnetic').forEach((chip) => {
      const toX = gsap.quickTo(chip, 'x', { duration: 0.4, ease: 'power3.out' })
      const toY = gsap.quickTo(chip, 'y', { duration: 0.4, ease: 'power3.out' })
      const onMove = (e: PointerEvent) => {
        const r = chip.getBoundingClientRect()
        toX((e.clientX - r.left - r.width / 2) * 0.25)
        toY((e.clientY - r.top - r.height / 2) * 0.35)
      }
      const onLeave = () => {
        toX(0)
        toY(0)
      }
      chip.addEventListener('pointermove', onMove)
      chip.addEventListener('pointerleave', onLeave)
      disposers.push(() => {
        chip.removeEventListener('pointermove', onMove)
        chip.removeEventListener('pointerleave', onLeave)
        gsap.set(chip, { x: 0, y: 0 })
      })
    })
  }

  function initPainStagger(gsap: typeof import('gsap').gsap) {
    const grid = refs.painGrid.value
    if (!grid || !('IntersectionObserver' in window)) return
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) return
        gsap.from(grid.children, {
          y: 26,
          opacity: 0,
          duration: 0.7,
          stagger: 0.09,
          ease: 'power3.out',
          clearProps: 'all'
        })
        io.disconnect()
      },
      { threshold: 0.15 }
    )
    io.observe(grid)
    disposers.push(() => io.disconnect())
  }

  function initRouteFlow(THREE: typeof import('three')) {
    const canvas = refs.routeCanvas.value
    const srcEl = refs.routeSrc.value
    const dstEl = refs.routeDst.value
    if (!canvas || !srcEl || !dstEl) return
    let renderer: import('three').WebGLRenderer
    try {
      renderer = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: true })
    } catch {
      canvas.style.display = 'none'
      return
    }

    const scene = new THREE.Scene()
    const camera = new THREE.OrthographicCamera(0, 1, 1, 0, -10, 10)

    const COUNT = 26
    const geo = new THREE.BufferGeometry()
    const positions = new Float32Array(COUNT * 3)
    geo.setAttribute('position', new THREE.BufferAttribute(positions, 3))
    // Round glow sprite so particles do not render as squares
    const spriteCanvas = document.createElement('canvas')
    spriteCanvas.width = spriteCanvas.height = 32
    const spriteCtx = spriteCanvas.getContext('2d')
    if (spriteCtx) {
      const grad = spriteCtx.createRadialGradient(16, 16, 0, 16, 16, 16)
      grad.addColorStop(0, 'rgba(255,255,255,1)')
      grad.addColorStop(0.5, 'rgba(255,255,255,0.55)')
      grad.addColorStop(1, 'rgba(255,255,255,0)')
      spriteCtx.fillStyle = grad
      spriteCtx.fillRect(0, 0, 32, 32)
    }
    const spriteTex = new THREE.CanvasTexture(spriteCanvas)
    const mat = new THREE.PointsMaterial({
      size: 7,
      map: spriteTex,
      transparent: true,
      opacity: 0.9,
      depthWrite: false
    })
    scene.add(new THREE.Points(geo, mat))

    const tune = () => mat.color.set(isDark.value ? '#5eead4' : '#0d9488')
    tune()
    themeTuners.push(tune)

    const particles = Array.from({ length: COUNT }, () => ({
      t: Math.random(),
      lane: Math.floor(Math.random() * 4),
      speed: 0.0035 + Math.random() * 0.004
    }))

    const resize = () => {
      const w = canvas.clientWidth || 1
      const h = canvas.clientHeight || 1
      renderer.setSize(w, h, false)
      renderer.setPixelRatio(Math.min(2, window.devicePixelRatio || 1))
      camera.right = w
      camera.top = h
      camera.updateProjectionMatrix()
    }
    resize()
    window.addEventListener('resize', resize)

    let visible = true
    let io: IntersectionObserver | null = null
    if (typeof IntersectionObserver === 'function') {
      io = new IntersectionObserver((entries) => {
        visible = entries[0]?.isIntersecting ?? true
      })
      io.observe(canvas)
    }

    let rafId = 0
    const frame = () => {
      rafId = requestAnimationFrame(frame)
      if (!visible) return
      const cr = canvas.getBoundingClientRect()
      const sr = srcEl.getBoundingClientRect()
      const dr = dstEl.getBoundingClientRect()
      const h = canvas.clientHeight
      const x0 = sr.right - cr.left
      const y0 = sr.top - cr.top + sr.height / 2
      const x1 = dr.left - cr.left
      const y1 = dr.top - cr.top + dr.height / 2
      for (let i = 0; i < COUNT; i++) {
        const pt = particles[i]
        pt.t += pt.speed
        if (pt.t > 1) {
          pt.t = 0
          pt.lane = Math.floor(Math.random() * 4)
        }
        const bow = (pt.lane - 1.5) * 14
        const cx = (x0 + x1) / 2
        const cy = (y0 + y1) / 2 + bow
        const t = pt.t
        const it = 1 - t
        positions[i * 3] = it * it * x0 + 2 * it * t * cx + t * t * x1
        positions[i * 3 + 1] = h - (it * it * y0 + 2 * it * t * cy + t * t * y1)
        positions[i * 3 + 2] = 0
      }
      geo.attributes.position.needsUpdate = true
      renderer.render(scene, camera)
    }
    frame()

    disposers.push(() => {
      cancelAnimationFrame(rafId)
      window.removeEventListener('resize', resize)
      io?.disconnect()
      geo.dispose()
      mat.dispose()
      spriteTex.dispose()
      renderer.dispose()
    })
  }

  function initStepLine(
    gsap: typeof import('gsap').gsap,
    ScrollTrigger: typeof import('gsap/ScrollTrigger').ScrollTrigger
  ) {
    const path = refs.stepLine.value
    const section = refs.stepsSection.value
    if (!path || !section) return
    const lineTween = gsap.fromTo(
      path,
      { strokeDasharray: 1, strokeDashoffset: 1 },
      {
        strokeDashoffset: 0,
        duration: 1.4,
        ease: 'power2.inOut',
        scrollTrigger: { trigger: section, start: 'top 65%' }
      }
    )
    const dotTween = gsap.from(section.querySelectorAll('.step-dot'), {
      scale: 0.4,
      opacity: 0,
      duration: 0.55,
      stagger: 0.35,
      ease: 'back.out(2)',
      scrollTrigger: { trigger: section, start: 'top 65%' }
    })
    disposers.push(() => {
      lineTween.scrollTrigger?.kill()
      lineTween.kill()
      dotTween.scrollTrigger?.kill()
      dotTween.kill()
      ScrollTrigger.refresh()
    })
  }

  function initCtaSpotlight() {
    const card = refs.ctaCard.value
    if (!card) return
    const onMove = (e: PointerEvent) => {
      const r = card.getBoundingClientRect()
      card.style.setProperty('--sx', `${((e.clientX - r.left) / r.width) * 100}%`)
      card.style.setProperty('--sy', `${((e.clientY - r.top) / r.height) * 100}%`)
    }
    card.addEventListener('pointermove', onMove)
    disposers.push(() => card.removeEventListener('pointermove', onMove))
  }
}
