import type {CSSProperties, ReactNode} from 'react';
import {
  AbsoluteFill,
  Easing,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from 'remotion';

export const palette = {
  background: '#070A14',
  panel: '#101626',
  panelStrong: '#161E33',
  text: '#F7F9FF',
  muted: '#9AA6C1',
  line: '#2A3551',
  source: '#42C8FF',
  core: '#8B6CFF',
  plugin: '#35D07F',
  data: '#F5B942',
  output: '#FF7474',
};

export const fontFamily =
  'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';

const clamp = {
  extrapolateLeft: 'clamp' as const,
  extrapolateRight: 'clamp' as const,
};

export const reveal = (frame: number, fps: number, delaySeconds = 0) =>
  spring({
    frame: frame - delaySeconds * fps,
    fps,
    config: {damping: 200},
    durationInFrames: 0.7 * fps,
  });

export const Background = ({accent = palette.core}: {accent?: string}) => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const drift = interpolate(frame, [0, 12 * fps], [-4, 7], clamp);

  return (
    <AbsoluteFill
      style={{
        backgroundColor: palette.background,
        backgroundImage: [
          `radial-gradient(circle at ${18 + drift}% 12%, ${accent}22 0, transparent 34%)`,
          `radial-gradient(circle at 82% ${88 - drift / 2}%, ${palette.source}14 0, transparent 30%)`,
          'linear-gradient(rgba(255,255,255,0.024) 1px, transparent 1px)',
          'linear-gradient(90deg, rgba(255,255,255,0.024) 1px, transparent 1px)',
        ].join(', '),
        backgroundSize: 'auto, auto, 48px 48px, 48px 48px',
        fontFamily,
        color: palette.text,
      }}
    />
  );
};

export const Brand = () => (
  <div
    style={{
      position: 'absolute',
      left: 64,
      top: 48,
      display: 'flex',
      alignItems: 'center',
      gap: 14,
      fontSize: 24,
      fontWeight: 760,
      letterSpacing: '-0.02em',
      zIndex: 20,
    }}
  >
    <div
      style={{
        width: 42,
        height: 42,
        borderRadius: 13,
        display: 'grid',
        placeItems: 'center',
        background: `linear-gradient(145deg, ${palette.core}, #5B42D6)`,
        boxShadow: `0 12px 36px ${palette.core}45`,
        fontSize: 24,
      }}
    >
      Q
    </div>
    Quivr V2
  </div>
);

export const Kicker = ({children, color = palette.source}: {children: ReactNode; color?: string}) => (
  <div
    style={{
      display: 'inline-flex',
      alignItems: 'center',
      gap: 10,
      padding: '9px 16px',
      borderRadius: 999,
      border: `1px solid ${color}55`,
      color,
      background: `${color}12`,
      fontSize: 17,
      fontWeight: 760,
      textTransform: 'uppercase',
      letterSpacing: '0.12em',
    }}
  >
    <span style={{width: 8, height: 8, borderRadius: 99, background: color}} />
    {children}
  </div>
);

export const Title = ({
  children,
  size = 70,
  maxWidth = 1400,
}: {
  children: ReactNode;
  size?: number;
  maxWidth?: number;
}) => (
  <div
    style={{
      fontSize: size,
      lineHeight: 1.02,
      fontWeight: 820,
      letterSpacing: '-0.055em',
      maxWidth,
    }}
  >
    {children}
  </div>
);

export const Icon = ({name, color}: {name: string; color: string}) => {
  const common = {
    width: 54,
    height: 54,
    viewBox: '0 0 54 54',
    fill: 'none',
    stroke: color,
    strokeWidth: 2.8,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
  };

  if (name === 'content') {
    return (
      <svg {...common}>
        <path d="M14 7h18l9 9v31H14z" />
        <path d="M32 7v10h9M20 27h15M20 34h11" />
      </svg>
    );
  }
  if (name === 'api') {
    return (
      <svg {...common}>
        <path d="M12 18h30v24H12z" />
        <path d="M18 12v6M36 12v6M19 29h16M22 35h10" />
      </svg>
    );
  }
  if (name === 'workflow') {
    return (
      <svg {...common}>
        <circle cx="14" cy="27" r="6" />
        <circle cx="40" cy="14" r="6" />
        <circle cx="40" cy="40" r="6" />
        <path d="M20 25l14-8M20 29l14 8" />
      </svg>
    );
  }
  if (name === 'plugin') {
    return (
      <svg {...common}>
        <path d="M11 30h10v-8a6 6 0 0112 0v8h10v13H11z" />
        <path d="M16 15l3-3M38 15l-3-3M27 10V6" />
      </svg>
    );
  }
  if (name === 'store') {
    return (
      <svg {...common}>
        <ellipse cx="27" cy="13" rx="16" ry="7" />
        <path d="M11 13v14c0 4 7 7 16 7s16-3 16-7V13M11 27v14c0 4 7 7 16 7s16-3 16-7V27" />
      </svg>
    );
  }
  if (name === 'search') {
    return (
      <svg {...common}>
        <circle cx="23" cy="23" r="13" />
        <path d="M33 33l12 12M18 23h10M23 18v10" />
      </svg>
    );
  }
  return (
    <svg {...common}>
      <path d="M15 23a12 12 0 0124 0c0 14 6 14 6 18H9c0-4 6-4 6-18z" />
      <path d="M22 46h10" />
    </svg>
  );
};

type StageCardProps = {
  x: number;
  y: number;
  width: number;
  height: number;
  color: string;
  icon: string;
  label: string;
  detail: string;
  entranceSeconds: number;
  active: boolean;
  badge?: string;
};

export const StageCard = ({
  x,
  y,
  width,
  height,
  color,
  icon,
  label,
  detail,
  entranceSeconds,
  active,
  badge,
}: StageCardProps) => {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const entered = reveal(frame, fps, entranceSeconds);
  const activeProgress = spring({
    frame: active ? frame : -100,
    fps,
    config: {damping: 200},
    durationInFrames: 0.25 * fps,
  });

  return (
    <div
      style={{
        position: 'absolute',
        left: x,
        top: y + (1 - entered) * 30,
        width,
        height,
        boxSizing: 'border-box',
        padding: width < 160 ? '18px 14px' : '24px 22px',
        borderRadius: 24,
        border: `1px solid ${active ? color : palette.line}`,
        background: active
          ? `linear-gradient(155deg, ${color}24, ${palette.panelStrong} 72%)`
          : `linear-gradient(155deg, ${palette.panelStrong}, ${palette.panel})`,
        boxShadow: active
          ? `0 0 ${30 + activeProgress * 28}px ${color}35, inset 0 1px 0 rgba(255,255,255,.08)`
          : 'inset 0 1px 0 rgba(255,255,255,.05)',
        opacity: entered,
        transform: `scale(${0.94 + entered * 0.06 + activeProgress * 0.02})`,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'space-between',
        zIndex: 4,
      }}
    >
      <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start'}}>
        <Icon name={icon} color={color} />
        {badge ? (
          <span
            style={{
              color,
              border: `1px solid ${color}55`,
              background: `${color}12`,
              padding: '6px 9px',
              borderRadius: 999,
              fontSize: 12,
              fontWeight: 800,
              letterSpacing: '0.08em',
              textTransform: 'uppercase',
            }}
          >
            {badge}
          </span>
        ) : null}
      </div>
      <div>
        <div style={{fontSize: width < 160 ? 20 : 24, fontWeight: 790, letterSpacing: '-0.025em'}}>{label}</div>
        {detail ? <div style={{fontSize: 15, color: palette.muted, marginTop: 9, lineHeight: 1.35}}>{detail}</div> : null}
      </div>
    </div>
  );
};

export const FlowLine = ({
  x1,
  x2,
  y,
  color = palette.line,
  progress = 1,
}: {
  x1: number;
  x2: number;
  y: number;
  color?: string;
  progress?: number;
}) => (
  <svg
    style={{position: 'absolute', inset: 0, overflow: 'visible', zIndex: 2}}
    width="100%"
    height="100%"
  >
    <line
      x1={x1}
      x2={x1 + (x2 - x1) * progress}
      y1={y}
      y2={y}
      stroke={color}
      strokeWidth={4}
      strokeLinecap="round"
    />
    {progress > 0.98 ? <path d={`M ${x2 - 10} ${y - 8} L ${x2} ${y} L ${x2 - 10} ${y + 8}`} stroke={color} strokeWidth={4} fill="none" strokeLinecap="round" /> : null}
  </svg>
);

export const Payload = ({x, y, enriched}: {x: number; y: number; enriched?: boolean}) => {
  const frame = useCurrentFrame();
  const pulse = 1 + Math.sin(frame / 3.5) * 0.04;
  const color = enriched ? palette.plugin : palette.source;
  return (
    <div
      style={{
        position: 'absolute',
        left: x - 25,
        top: y - 25,
        width: 50,
        height: 50,
        borderRadius: 17,
        background: `linear-gradient(145deg, ${color}, ${color}BB)`,
        boxShadow: `0 0 34px ${color}99`,
        display: 'grid',
        placeItems: 'center',
        transform: `scale(${pulse})`,
        zIndex: 8,
      }}
    >
      <div style={{width: 17, height: 22, border: '2px solid #06101A', borderRadius: 3, position: 'relative'}}>
        <div style={{position: 'absolute', left: 3, right: 3, top: 6, height: 2, background: '#06101A'}} />
        <div style={{position: 'absolute', left: 3, right: 5, top: 11, height: 2, background: '#06101A'}} />
      </div>
    </div>
  );
};

export const sceneStyle: CSSProperties = {
  padding: '118px 64px 56px',
  boxSizing: 'border-box',
  color: palette.text,
  fontFamily,
};

export const smooth = (frame: number, range: [number, number], output: [number, number]) =>
  interpolate(frame, range, output, {
    ...clamp,
    easing: Easing.inOut(Easing.cubic),
  });
