<script lang="ts">
  import { loadAppearance, saveAppearance, textScales, themes, type StudioTheme, type TextScale } from '../appearance';

  const labels: Record<StudioTheme, string> = { light: 'Light', dawn: 'Dawn', night: 'Night' };
  const descriptions: Record<StudioTheme, string> = {
    light: 'Warm paper, forest accents, and crisp ink',
    dawn: 'Blue slate, sea-glass accents, and soft white text',
    night: 'Quiet graphite, sage accents, and clear contrast'
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
          <span class={`theme-preview theme-preview-${option}`} aria-hidden="true">
            <span class="preview-rail"><b>Q</b><i></i><i></i><i></i></span>
            <span class="preview-page"><b>Aa</b><span class="preview-line"></span><span class="preview-line short"></span><span class="preview-card"><i></i><i></i></span><span class="preview-action"></span></span>
          </span>
          <strong>{labels[option]}</strong><small>{descriptions[option]}</small>
        </label>
      {/each}
    </div>
  </fieldset>

  <fieldset class="appearance-group">
    <legend>Text size</legend>
    <p>Choose the reading size that feels comfortable. Your selection applies across Studio.</p>
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
  .theme-option { display: grid; align-content: start; gap: 8px; min-width: 0; padding: 16px; border: 1px solid var(--border-strong); border-radius: 10px; background: var(--surface); cursor: pointer; }
  .theme-option.selected, .text-scale-options label.selected { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .theme-option:focus-within, .text-scale-options label:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .theme-option input, .text-scale-options input { position: absolute; width: 1px; height: 1px; opacity: 0; }
  .theme-option strong { color: var(--text); }
  .theme-option small { color: var(--muted); font-size: var(--type-small); line-height: 1.55; }
  .theme-preview { display: grid; grid-template-columns: 32px 1fr; height: 150px; overflow: hidden; margin-bottom: 10px; border: 1px solid var(--border); border-radius: 7px; background: var(--background); color: var(--text); }
  .preview-rail { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 12px 6px; border-right: 1px solid var(--border); background: var(--sidebar-bg); }
  .preview-rail b { color: var(--accent-text); font-size: 17px; line-height: 1; }
  .preview-rail i { width: 10px; height: 5px; border-radius: 2px; background: var(--border-strong); }
  .preview-rail i:first-of-type { width: 20px; height: 16px; background: var(--accent-soft); border: 1px solid var(--accent-border); }
  .preview-page { display: flex; flex-direction: column; align-items: start; gap: 7px; padding: 13px; }
  .preview-page > b { font-size: 20px; line-height: 1.1; font-weight: 600; }
  .preview-line { width: 85%; height: 3px; background: var(--muted); border-radius: 2px; }
  .preview-line.short { width: 56%; background: var(--subtle); }
  .preview-card { display: grid; gap: 6px; width: 100%; padding: 10px; border: 1px solid var(--border); border-radius: 4px; background: var(--surface); }
  .preview-card i { width: 70%; height: 3px; background: var(--muted); border-radius: 2px; }
  .preview-card i:last-child { width: 46%; background: var(--subtle); }
  .preview-action { width: 35%; height: 12px; border-radius: 3px; background: var(--accent); }
  .text-scale-options { display: flex; flex-wrap: wrap; gap: 10px; }
  .text-scale-options label { min-width: 80px; padding: 10px 14px; border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface); color: var(--text); text-align: center; cursor: pointer; }
  @media (max-width: 760px) { .theme-options { grid-template-columns: 1fr; } .theme-option { grid-template-columns: 90px 1fr; align-items: center; } .theme-preview { grid-row: span 2; height: 62px; margin: 0; } }
</style>
