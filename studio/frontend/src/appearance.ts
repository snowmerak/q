import { uiStorage } from './uiStorage';

export type StudioTheme = 'light' | 'dawn' | 'night' | 'graphite';
export type TextScale = 100 | 115 | 130 | 145;

type Appearance = {
  theme: StudioTheme;
  textScale: TextScale;
  compactSessions: boolean;
};

export const themes: StudioTheme[] = ['light', 'dawn', 'night', 'graphite'];
export const textScales: TextScale[] = [100, 115, 130, 145];

const themeKey = 'q-studio-theme';
const textScaleKey = 'q-studio-text-scale';
const compactSessionsKey = 'q-studio-compact-sessions';

export function loadAppearance(): Appearance {
  const savedTheme = uiStorage.getItem(themeKey);
  const savedScale = Number(uiStorage.getItem(textScaleKey));
  return {
    theme: themes.includes(savedTheme as StudioTheme) ? savedTheme as StudioTheme : 'dawn',
    textScale: textScales.includes(savedScale as TextScale) ? savedScale as TextScale : 100,
    compactSessions: uiStorage.getItem(compactSessionsKey) === 'true'
  };
}

export function applyAppearance({ theme, textScale, compactSessions }: Appearance) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.classList.toggle('compact-sessions', compactSessions);
  document.documentElement.style.setProperty('--text-scale', String(textScale / 100));
  document.documentElement.style.colorScheme = theme === 'light' ? 'light' : 'dark';
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content',
    getComputedStyle(document.documentElement).getPropertyValue('--background').trim());
}

export function saveAppearance({ theme, textScale, compactSessions }: Partial<Appearance>) {
  if (theme !== undefined) uiStorage.setItem(themeKey, theme);
  if (textScale !== undefined) uiStorage.setItem(textScaleKey, String(textScale));
  if (compactSessions !== undefined) uiStorage.setItem(compactSessionsKey, String(compactSessions));
  applyAppearance(loadAppearance());
}
