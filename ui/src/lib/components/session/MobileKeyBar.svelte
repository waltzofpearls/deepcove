<script lang="ts">
  // The keys an on-screen keyboard lacks, under the terminal on phones and tablets. It stays up with the
  // keyboard closed, so picking from an agent's menu takes arrows and Enter, and no typing.
  import type { CtrlState, MobileKey } from '$lib/util/mobileKeyBar';
  import ArrowDown from '@lucide/svelte/icons/arrow-down';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import ArrowLeftToLine from '@lucide/svelte/icons/arrow-left-to-line';
  import ArrowRight from '@lucide/svelte/icons/arrow-right';
  import ArrowRightToLine from '@lucide/svelte/icons/arrow-right-to-line';
  import ArrowUp from '@lucide/svelte/icons/arrow-up';
  import CornerDownLeft from '@lucide/svelte/icons/corner-down-left';
  import Keyboard from '@lucide/svelte/icons/keyboard';
  import KeyboardOff from '@lucide/svelte/icons/keyboard-off';

  let {
    ctrl,
    keyboardOpen,
    onKey,
    onCtrl,
    onKeyboard,
  }: {
    ctrl: CtrlState;
    keyboardOpen: boolean;
    onKey: (key: MobileKey) => void;
    onCtrl: () => void;
    onKeyboard: () => void;
  } = $props();

  const leftKeys = [
    { key: 'tab', icon: ArrowRightToLine, label: 'Tab' },
    { key: 'shiftTab', icon: ArrowLeftToLine, label: 'Shift Tab' },
  ] as const;

  const iconKeys = [
    { key: 'left', icon: ArrowLeft, label: 'Left' },
    { key: 'up', icon: ArrowUp, label: 'Up' },
    { key: 'down', icon: ArrowDown, label: 'Down' },
    { key: 'right', icon: ArrowRight, label: 'Right' },
    { key: 'enter', icon: CornerDownLeft, label: 'Enter' },
  ] as const;

  // Keys share the width like a row of Apple's keyboard, so a phone fits them all without scrolling. On an
  // iPad they stop growing at 64px.
  const keyClass =
    'flex h-10 min-w-0 max-w-16 flex-1 basis-0 items-center justify-center rounded-md border text-base shadow-sm active:translate-y-px active:shadow-none';
  const plainKeyClass = `${keyClass} border-border bg-secondary text-foreground active:bg-muted`;
  const ctrlClass = $derived(
    ctrl === 'locked'
      ? `${keyClass} border-primary bg-primary text-primary-foreground`
      : ctrl === 'once'
        ? `${keyClass} border-primary bg-secondary text-primary`
        : plainKeyClass,
  );

  // A tap must not move focus, or it would close the on-screen keyboard.
  function keepFocus(event: MouseEvent) {
    event.preventDefault();
  }
</script>

<div
  class="flex shrink-0 select-none justify-center gap-1.5 border-t border-border bg-card px-1.5 py-1"
  role="toolbar"
  aria-label="Terminal keys"
>
  <button
    type="button"
    class={plainKeyClass}
    aria-label="Escape"
    onmousedown={keepFocus}
    onclick={() => onKey('escape')}
  >
    esc
  </button>
  {#each leftKeys as item (item.key)}
    <button
      type="button"
      class={plainKeyClass}
      aria-label={item.label}
      onmousedown={keepFocus}
      onclick={() => onKey(item.key)}
    >
      <item.icon class="size-5" />
    </button>
  {/each}
  <button
    type="button"
    class={ctrlClass}
    aria-label="Control"
    aria-pressed={ctrl !== 'off'}
    onmousedown={keepFocus}
    onclick={onCtrl}
  >
    ctrl
  </button>
  <span class="w-px shrink-0 self-stretch bg-border" aria-hidden="true"></span>
  {#each iconKeys as item (item.key)}
    <button
      type="button"
      class={plainKeyClass}
      aria-label={item.label}
      onmousedown={keepFocus}
      onclick={() => onKey(item.key)}
    >
      <item.icon class="size-5" />
    </button>
  {/each}
  <button
    type="button"
    class={plainKeyClass}
    aria-label={keyboardOpen ? 'Hide keyboard' : 'Show keyboard'}
    onmousedown={keepFocus}
    onclick={onKeyboard}
  >
    {#if keyboardOpen}
      <KeyboardOff class="size-5" />
    {:else}
      <Keyboard class="size-5" />
    {/if}
  </button>
</div>
