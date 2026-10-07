export type StudioTheme = 'light' | 'dawn' | 'night';
export type TextScale = 100 | 115 | 130 | 145;

export const themes: StudioTheme[] = ['light', 'dawn', 'night'];
export const textScales: TextScale[] = [100, 115, 130, 145];

const themeKey = 'q-studio-theme';
const textScaleKey = 'q-studio-text-scale';

export function loadAppearance(): { theme: StudioTheme; textScale: TextScale } {
  try {
    const savedTheme = localStorage.getItem(themeKey);
    const savedScale = Number(localStorage.getItem(textScaleKey));
    return {
      theme: themes.includes(savedTheme as StudioTheme) ? savedTheme as StudioTheme : 'dawn',
      textScale: textScales.includes(savedScale as TextScale) ? savedScale as TextScale : 100
    };
  } catch {
    return { theme: 'dawn', textScale: 100 };
  }
}

export function applyAppearance(theme: StudioTheme, textScale: TextScale) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.setProperty('--text-scale', String(textScale / 100));
  document.documentElement.style.colorScheme = theme === 'light' ? 'light' : 'dark';
}

export function saveAppearance(theme: StudioTheme, textScale: TextScale) {
  applyAppearance(theme, textScale);
  try {
    localStorage.setItem(themeKey, theme);
    localStorage.setItem(textScaleKey, String(textScale));
  } catch {
    // Keep the selection active for this page when storage is unavailable.
  }
}
