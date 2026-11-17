import { uiStorage } from './uiStorage';

export type StudioTheme = 'light' | 'dawn' | 'night';
export type TextScale = 100 | 115 | 130 | 145;

export const themes: StudioTheme[] = ['light', 'dawn', 'night'];
export const textScales: TextScale[] = [100, 115, 130, 145];

const themeKey = 'q-studio-theme';
const textScaleKey = 'q-studio-text-scale';

export function loadAppearance(): { theme: StudioTheme; textScale: TextScale } {
  const savedTheme = uiStorage.getItem(themeKey);
  const savedScale = Number(uiStorage.getItem(textScaleKey));
  return {
    theme: themes.includes(savedTheme as StudioTheme) ? savedTheme as StudioTheme : 'dawn',
    textScale: textScales.includes(savedScale as TextScale) ? savedScale as TextScale : 100
  };
}

export function applyAppearance(theme: StudioTheme, textScale: TextScale) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.setProperty('--text-scale', String(textScale / 100));
  document.documentElement.style.colorScheme = theme === 'light' ? 'light' : 'dark';
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content',
    getComputedStyle(document.documentElement).getPropertyValue('--background').trim());
}

export function saveAppearance(theme: StudioTheme, textScale: TextScale) {
  applyAppearance(theme, textScale);
  uiStorage.setItem(themeKey, theme);
  uiStorage.setItem(textScaleKey, String(textScale));
}
