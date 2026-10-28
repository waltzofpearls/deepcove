<script lang="ts">
  // How a session logs in, as one choice since the ways rule each other out.
  import type { LoginMode } from '$lib/api/types';
  import type { LoginChoice } from '$lib/util/loginMode';

  let {
    choices,
    value,
    defaultMode,
    disabled = false,
    onpick,
  }: {
    choices: LoginChoice[];
    value: LoginMode;
    // defaultMode gets a "default" tag, so you can see what the node would pick.
    defaultMode?: LoginMode;
    disabled?: boolean;
    onpick: (mode: LoginMode) => void;
  } = $props();

  const picked = $derived(choices.find((choice) => choice.mode === value));
</script>

<fieldset class="space-y-2" {disabled}>
  <legend class="mb-2 text-sm font-medium">How this session logs in</legend>
  <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
    {#each choices as choice (choice.mode)}
      <label
        title={choice.address}
        class="flex min-w-0 cursor-pointer items-start gap-2.5 rounded-md border border-border px-3 py-2.5 has-[:checked]:border-primary has-[:checked]:bg-primary/5 has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60"
      >
        <input
          type="radio"
          name="login-mode"
          class="mt-1"
          value={choice.mode}
          checked={value === choice.mode}
          disabled={disabled || !!choice.unavailable}
          onchange={() => onpick(choice.mode)}
        />
        <span class="flex min-w-0 flex-col gap-0.5">
          <span class="flex min-w-0 items-baseline gap-2 text-sm">
            <span class="shrink-0">{choice.label}</span>
            {#if choice.mode === defaultMode}
              <span class="shrink-0 text-[11px] font-semibold text-primary">default</span>
            {/if}
            {#if choice.address}
              <span class="min-w-0 truncate font-mono text-[11px] text-muted-foreground">
                {choice.address}
              </span>
            {/if}
          </span>
          {#if choice.unavailable}
            <span class="text-xs text-muted-foreground">{choice.unavailable}</span>
          {/if}
        </span>
      </label>
    {/each}
  </div>
  {#if picked}
    <p class="text-xs text-muted-foreground [overflow-wrap:anywhere]">{picked.detail}</p>
  {/if}
</fieldset>
