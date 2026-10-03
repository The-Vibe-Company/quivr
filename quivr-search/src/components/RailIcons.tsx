import type { ReactNode } from "react";

/**
 * Icônes de la barre latérale, reprises de Tabler Icons (MIT, tabler.io/icons) :
 * news, radar-2, antenna, chart-dots-3, search, plus, sun, moon, calendar,
 * chevron-down, chevron-up, chevrons-right, external-link, check, eye et
 * eye-off.
 * Dessinées au trait, elles prennent la couleur du texte.
 */
function Icon({ size = 22, children }: { size?: number; children: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

export const FeedIcon = ({ size }: { size?: number }) => (
  <Icon size={size}>
    <path d="M16 6h3a1 1 0 0 1 1 1v11a2 2 0 0 1 -4 0v-13a1 1 0 0 0 -1 -1h-10a1 1 0 0 0 -1 1v12a3 3 0 0 0 3 3h11" />
    <path d="M8 8l4 0" />
    <path d="M8 12l4 0" />
    <path d="M8 16l4 0" />
  </Icon>
);

export const AlertsIcon = ({ size }: { size?: number }) => (
  <Icon size={size}>
    <path d="M11 12a1 1 0 1 0 2 0a1 1 0 1 0 -2 0" />
    <path d="M15.51 15.56a5 5 0 1 0 -3.51 1.44" />
    <path d="M18.832 17.86a9 9 0 1 0 -6.832 3.14" />
    <path d="M12 12v9" />
  </Icon>
);

export const SourcesIcon = ({ size }: { size?: number }) => (
  <Icon size={size}>
    <path d="M20 4v8" />
    <path d="M16 4.5v7" />
    <path d="M12 5v16" />
    <path d="M8 5.5v5" />
    <path d="M4 6v4" />
    <path d="M20 8h-16" />
  </Icon>
);

export const AdminIcon = ({ size }: { size?: number }) => (
  <Icon size={size}>
    <path d="M3 7a2 2 0 1 0 4 0a2 2 0 1 0 -4 0" />
    <path d="M14 15a2 2 0 1 0 4 0a2 2 0 1 0 -4 0" />
    <path d="M15 6a3 3 0 1 0 6 0a3 3 0 1 0 -6 0" />
    <path d="M3 18a3 3 0 1 0 6 0a3 3 0 1 0 -6 0" />
    <path d="M9 17l5 -1.5" />
    <path d="M6.5 8.5l7.81 5.37" />
    <path d="M7 7l8 -1" />
  </Icon>
);

export const SearchIcon = ({ size = 17 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M3 10a7 7 0 1 0 14 0a7 7 0 1 0 -14 0" />
    <path d="M21 21l-6 -6" />
  </Icon>
);

export const PlusIcon = ({ size = 21 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M12 5l0 14" />
    <path d="M5 12l14 0" />
  </Icon>
);

export const SunIcon = ({ size = 20 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M12 12m-4 0a4 4 0 1 0 8 0a4 4 0 1 0 -8 0" />
    <path d="M3 12h1m8 -9v1m8 8h1m-9 8v1m-6.4 -15.4l.7 .7m12.1 -.7l-.7 .7m0 11.4l.7 .7m-12.1 -.7l-.7 .7" />
  </Icon>
);

export const MoonIcon = ({ size = 20 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M12 3c.132 0 .263 0 .393 0a7.5 7.5 0 0 0 7.92 12.446a9 9 0 1 1 -8.313 -12.454z" />
  </Icon>
);

export const CalendarIcon = ({ size = 15 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M4 7a2 2 0 0 1 2 -2h12a2 2 0 0 1 2 2v12a2 2 0 0 1 -2 2h-12a2 2 0 0 1 -2 -2v-12z" />
    <path d="M16 3v4" />
    <path d="M8 3v4" />
    <path d="M4 11h16" />
  </Icon>
);

export const ChevronDownIcon = ({ size = 14 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M6 9l6 6l6 -6" />
  </Icon>
);

export const ChevronUpIcon = ({ size = 16 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M6 15l6 -6l6 6" />
  </Icon>
);

export const ChevronsRightIcon = ({ size = 18 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M7 7l5 5l-5 5" />
    <path d="M13 7l5 5l-5 5" />
  </Icon>
);

export const ExternalLinkIcon = ({ size = 15 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M12 6h-6a2 2 0 0 0 -2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2 -2v-6" />
    <path d="M11 13l9 -9" />
    <path d="M15 4h5v5" />
  </Icon>
);

export const CheckIcon = ({ size = 14 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M5 12l5 5l10 -10" />
  </Icon>
);

export const EyeIcon = ({ size = 16 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M10 12a2 2 0 1 0 4 0a2 2 0 0 0 -4 0" />
    <path d="M21 12c-2.4 4 -5.4 6 -9 6c-3.6 0 -6.6 -2 -9 -6c2.4 -4 5.4 -6 9 -6c3.6 0 6.6 2 9 6" />
  </Icon>
);

export const EyeOffIcon = ({ size = 16 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M10.585 10.587a2 2 0 0 0 2.829 2.828" />
    <path d="M16.681 16.673a8.717 8.717 0 0 1 -4.681 1.327c-3.6 0 -6.6 -2 -9 -6c1.272 -2.12 2.712 -3.678 4.32 -4.674m2.86 -1.146a9.055 9.055 0 0 1 1.82 -.18c3.6 0 6.6 2 9 6c-.666 1.11 -1.379 2.067 -2.138 2.87" />
    <path d="M3 3l18 18" />
  </Icon>
);
