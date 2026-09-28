<script lang="ts">
  import { afterUpdate, onMount } from 'svelte';
  import { renderMarkdown } from './markdown';

  export let content = '';
  export let compact = false;

  let root: HTMLDivElement;
  $: rendered = renderMarkdown(content);

  function decorateCodeBlocks() {
    if (!root) return;
    for (const block of root.querySelectorAll<HTMLPreElement>('pre:not([data-studio-code])')) {
      block.dataset.studioCode = 'true';
      const code = block.querySelector('code');
      if (!code) continue;
      const language = [...code.classList].find((name) => name.startsWith('language-'))?.slice(9) || block.dataset.language || 'text';
      const frame = document.createElement('div');
      frame.className = 'code-frame';
      const toolbar = document.createElement('div');
      toolbar.className = 'code-toolbar';
      const label = document.createElement('span');
      label.textContent = language;
      const copy = document.createElement('button');
      copy.type = 'button';
      copy.className = 'code-copy';
      copy.textContent = 'Copy';
      copy.setAttribute('aria-label', `Copy ${language} code`);
      toolbar.append(label, copy);
      block.parentNode?.insertBefore(frame, block);
      frame.append(toolbar, block);
    }
  }

  async function handleClick(event: MouseEvent) {
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('.code-copy') : null;
    if (!target || !root.contains(target)) return;
    const code = target.closest('.code-frame')?.querySelector('code')?.textContent || '';
    try {
      await navigator.clipboard.writeText(code);
      target.textContent = 'Copied';
      window.setTimeout(() => {
        if (target.isConnected) target.textContent = 'Copy';
      }, 1400);
    } catch {
      target.textContent = 'Copy failed';
    }
  }

  onMount(() => {
    root.addEventListener('click', handleClick);
    decorateCodeBlocks();
    return () => root.removeEventListener('click', handleClick);
  });

  afterUpdate(decorateCodeBlocks);
</script>

<div class="markdown-body" class:compact bind:this={root}>{@html rendered}</div>
