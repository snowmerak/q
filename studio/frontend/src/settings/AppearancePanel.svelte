<script lang="ts">
  import { loadAppearance, saveAppearance, textScales, themes, type StudioTheme, type TextScale } from '../appearance';

  const labels: Record<StudioTheme, string> = { light: 'Light', dawn: 'Dawn', night: 'Night' };
  const descriptions: Record<StudioTheme, string> = {
    light: 'Bright surfaces with clear dark text',
    dawn: 'The current violet and midnight palette',
    night: 'Deeper contrast for dark rooms'
  };

  let { theme, textScale } = loadAppearance();

  function chooseTheme(value: StudioTheme) {
    theme = value;
    saveAppearance(theme, textScale);
  }

  function chooseTextScale(value: TextScale) {
    textScale = value;
    saveAppearance(theme, textScale);
  }
</script>

<section class="settings-section appearance-section" aria-labelledby="appearance-heading">
  <div class="section-heading"><div><p class="eyebrow">PERSONAL DISPLAY</p><h2 id="appearance-heading">Appearance</h2><p>Choose colors and a comfortable reading size for this browser.</p></div></div>

  <fieldset class="appearance-group">
    <legend>Theme</legend>
    <div class="theme-options">
      {#each themes as option}
        <label class="theme-option" class:selected={theme === option}>
          <input type="radio" name="studio-theme" value={option} checked={theme === option} onchange={() => chooseTheme(option)} />
          <span class={`theme-preview theme-preview-${option}`} aria-hidden="true"><span></span><span></span><span></span></span>
          <strong>{labels[option]}</strong><small>{descriptions[option]}</small>
        </label>
      {/each}
    </div>
  </fieldset>

  <fieldset class="appearance-group">
    <legend>Text size</legend>
    <p>100% is the current size. Choose a larger size for easier reading.</p>
    <div class="text-scale-options">
      {#each textScales as option}
        <label class:selected={textScale === option}>
          <input type="radio" name="studio-text-scale" value={option} checked={textScale === option} onchange={() => chooseTextScale(option)} />
          {option}%
        </label>
      {/each}
    </div>
  </fieldset>
</section>

<style>
  .appearance-group { min-width: 0; margin: 24px 0 0; padding: 0; border: 0; }
  .appearance-group legend { margin-bottom: 12px; color: var(--text); font-weight: 600; }
  .appearance-group p { margin-bottom: 14px; color: var(--muted); }
  .theme-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
  .theme-option { display: grid; align-content: start; gap: 6px; min-width: 0; padding: 12px; border: 1px solid var(--border-strong); border-radius: 7px; background: var(--surface); cursor: pointer; }
  .theme-option.selected, .text-scale-options label.selected { border-color: var(--violet); box-shadow: 0 0 0 1px var(--violet); }
  .theme-option:focus-within, .text-scale-options label:focus-within { outline: 2px solid var(--violet); outline-offset: 2px; }
  .theme-option input, .text-scale-options input { position: absolute; width: 1px; height: 1px; opacity: 0; }
  .theme-option strong { color: var(--text); }
  .theme-option small { color: var(--muted); line-height: 1.4; }
  .theme-preview { display: flex; height: 76px; align-items: center; gap: 8px; margin-bottom: 4px; padding: 10px; border: 1px solid; border-radius: 5px; }
  .theme-preview span:first-child { width: 24%; height: 100%; border-radius: 3px; }
  .theme-preview span:nth-child(2) { width: 48%; height: 44%; border: 1px solid; border-radius: 3px; }
  .theme-preview span:last-child { width: 12%; height: 12%; border-radius: 50%; }
  .theme-preview-light { color: #344154; border-color: #cbd3df; background: #f7f8fb; }
  .theme-preview-light span:first-child { background: #e9edf3; }
  .theme-preview-light span:nth-child(2) { border-color: #cbd3df; background: #fff; }
  .theme-preview-light span:last-child { background: #7145d2; }
  .theme-preview-dawn { color: #e7e9f4; border-color: #343c4e; background: #090b12; }
  .theme-preview-dawn span:first-child { background: #111521; }
  .theme-preview-dawn span:nth-child(2) { border-color: #343c4e; background: #0d1019; }
  .theme-preview-dawn span:last-child { background: #8f5cff; }
  .theme-preview-night { color: #f4f5f9; border-color: #3c4350; background: #020304; }
  .theme-preview-night span:first-child { background: #0b0c10; }
  .theme-preview-night span:nth-child(2) { border-color: #3c4350; background: #13151a; }
  .theme-preview-night span:last-child { background: #b49aff; }
  .text-scale-options { display: flex; flex-wrap: wrap; gap: 10px; }
  .text-scale-options label { min-width: 80px; padding: 10px 14px; border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface); color: var(--text); text-align: center; cursor: pointer; }
  @media (max-width: 760px) { .theme-options { grid-template-columns: 1fr; } .theme-option { grid-template-columns: 90px 1fr; align-items: center; } .theme-preview { grid-row: span 2; height: 62px; margin: 0; } }
</style>
