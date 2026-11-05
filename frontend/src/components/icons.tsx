import type { ReactNode, SVGProps } from "react";

type IconProps = SVGProps<SVGSVGElement>;

function Icon({ children, ...props }: IconProps & { children: ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className="h-6 w-6"
      {...props}
    >
      {children}
    </svg>
  );
}

export const MenuIcon = (p: IconProps) => (
  <Icon {...p}>
    <path d="M4 6h16M4 12h16M4 18h16" />
  </Icon>
);

export const SearchIcon = (p: IconProps) => (
  <Icon {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="m20 20-3.5-3.5" />
  </Icon>
);

export const HomeIcon = (p: IconProps) => (
  <Icon {...p}>
    <path d="m3 11 9-8 9 8" />
    <path d="M5 10v10h5v-6h4v6h5V10" />
  </Icon>
);

export const TrendingIcon = (p: IconProps) => (
  <Icon {...p}>
    <path d="M13 2 4 14h7l-1 8 9-12h-7l1-8z" />
  </Icon>
);

export const SubscriptionsIcon = (p: IconProps) => (
  <Icon {...p}>
    <rect x="3" y="7" width="18" height="13" rx="2" />
    <path d="M7 3.5h10" />
    <path d="m10 10.5 5 3-5 3z" />
  </Icon>
);

export const LibraryIcon = (p: IconProps) => (
  <Icon {...p}>
    <rect x="3" y="4" width="4" height="16" rx="1" />
    <rect x="10" y="4" width="4" height="16" rx="1" />
    <path d="m17 5 3.5 1-3.5 14" />
  </Icon>
);

export const HistoryIcon = (p: IconProps) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5l3 2" />
  </Icon>
);

export const UploadIcon = (p: IconProps) => (
  <Icon {...p}>
    <path d="M12 16V4" />
    <path d="m7 9 5-5 5 5" />
    <path d="M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3" />
  </Icon>
);

export const StudioIcon = (p: IconProps) => (
  <Icon {...p}>
    <rect x="3" y="6" width="13" height="12" rx="2" />
    <path d="m16 10.5 5-3v9l-5-3" />
  </Icon>
);

/** RyuCloud's mascot: a cartoon dragon head (also used as the favicon: see app/icon.svg). */
export function LogoMark({ className = "h-8 w-8" }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" aria-hidden="true" className={className}>
      <path d="M18 22 L11 6 L27 16 Z" fill="#f59e0b" stroke="#b45309" strokeWidth="1.5" strokeLinejoin="round"/>
      <path d="M46 22 L53 6 L37 16 Z" fill="#f59e0b" stroke="#b45309" strokeWidth="1.5" strokeLinejoin="round"/>
      <path d="M28 15 L32 7 L36 15 Z" fill="#fbbf24" stroke="#b45309" strokeWidth="1.5" strokeLinejoin="round"/>
      <path d="M11 30 L2 25 L7 40 Z" fill="#16a34a" stroke="#14532d" strokeWidth="1.5" strokeLinejoin="round"/>
      <path d="M53 30 L62 25 L57 40 Z" fill="#16a34a" stroke="#14532d" strokeWidth="1.5" strokeLinejoin="round"/>
      <ellipse cx="32" cy="35" rx="22" ry="20" fill="#22c55e" stroke="#14532d" strokeWidth="2"/>
      <ellipse cx="32" cy="44" rx="13" ry="9" fill="#86efac" stroke="#14532d" strokeWidth="1.5"/>
      <circle cx="27.5" cy="42.5" r="1.7" fill="#14532d"/>
      <circle cx="36.5" cy="42.5" r="1.7" fill="#14532d"/>
      <path d="M26 48.5 Q32 53 38 48.5" fill="none" stroke="#14532d" strokeWidth="1.8" strokeLinecap="round"/>
      <ellipse cx="22" cy="31" rx="6" ry="7" fill="#fff" stroke="#14532d" strokeWidth="1.5"/>
      <ellipse cx="42" cy="31" rx="6" ry="7" fill="#fff" stroke="#14532d" strokeWidth="1.5"/>
      <circle cx="23.5" cy="32" r="3.6" fill="#0f172a"/>
      <circle cx="40.5" cy="32" r="3.6" fill="#0f172a"/>
      <circle cx="24.7" cy="30.6" r="1.2" fill="#fff"/>
      <circle cx="41.7" cy="30.6" r="1.2" fill="#fff"/>
      <circle cx="14" cy="42" r="3.2" fill="#fb7185" opacity="0.55"/>
      <circle cx="50" cy="42" r="3.2" fill="#fb7185" opacity="0.55"/>
    </svg>
  );
}
