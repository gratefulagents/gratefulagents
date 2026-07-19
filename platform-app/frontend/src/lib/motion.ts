// Motion presets used across the app. Keeps motion consistent, subtle, fast.
// Referenced by framer-motion `transition` and CSS `transition-timing-function`.

export const ease = {
  outQuart: [0.25, 1, 0.5, 1] as [number, number, number, number],
  spring: [0.34, 1.3, 0.64, 1] as [number, number, number, number],
} as const;

export const duration = {
  fast: 0.12,
  base: 0.18,
  slow: 0.26,
} as const;

export const transitions = {
  subtle: { duration: duration.fast, ease: ease.outQuart },
  panel: { duration: duration.base, ease: ease.outQuart },
  springy: { type: "spring" as const, stiffness: 420, damping: 32, mass: 0.6 },
  slide: { duration: duration.base, ease: ease.outQuart },
};

export const fade = {
  initial: { opacity: 0 },
  animate: { opacity: 1 },
  exit: { opacity: 0 },
  transition: transitions.subtle,
};

export const lift = {
  initial: { opacity: 0, y: 4 },
  animate: { opacity: 1, y: 0 },
  exit: { opacity: 0, y: 4 },
  transition: transitions.panel,
};

export const palette = {
  initial: { opacity: 0, y: 6, scale: 0.985 },
  animate: { opacity: 1, y: 0, scale: 1 },
  exit: { opacity: 0, y: 4, scale: 0.99 },
  transition: transitions.springy,
};
