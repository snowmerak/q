<script lang="ts">
  import { ImagePlus, X } from '@lucide/svelte';
  import { appendImages, imageTypes } from './images';

  export let files: File[] = [];
  export let disabled = false;
  export let onerror: (text: string) => void;
  let input: HTMLInputElement;

  function read(event: Event) {
    const field = event.currentTarget as HTMLInputElement;
    try { files = appendImages(files, Array.from(field.files || [])); }
    catch (cause) { onerror((cause as Error).message); }
    field.value = '';
  }
</script>

<div class="image-picker">
  <input hidden bind:this={input} type="file" accept={imageTypes} multiple onchange={read} {disabled} />
  <button class="secondary-button" type="button" onclick={() => input.click()} aria-label="Attach images" title="Attach images" {disabled}><ImagePlus size={16} aria-hidden="true" /></button>
  {#each files as file, index}
    <button class="secondary-button" type="button" aria-label={`Remove ${file.name}`} {disabled} onclick={() => files = files.filter((_, i) => i !== index)}>{file.name}<X size={13} aria-hidden="true" /></button>
  {/each}
</div>

<style>
  .image-picker { display: flex; flex-wrap: wrap; gap: .4rem; }
  button { display: inline-flex; align-items: center; gap: .35rem; padding: .4rem .5rem; overflow-wrap: anywhere; }
</style>
