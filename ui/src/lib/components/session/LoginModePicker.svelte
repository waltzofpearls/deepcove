<script lang="ts">
  // How a session logs in, as one choice since the ways rule each other out.
  import type { LoginMode } from '$lib/api/types';
  import type { LoginChoice } from '$lib/util/loginMode';

  let {
    choices,
    value,
    disabled = false,
    onpick,
  }: {
    choices: LoginChoice[];
    value: LoginMode;
    disabled?: boolean;
    onpick: (mode: LoginMode) => void;
  } = $props();
</script>

<fieldset class="space-y-1.5" {disabled}>
  <legend class="mb-1.5 text-sm font-medium">How this session logs in</legend>
  {#each choices as choice (choice.mode)}
    <label
      class="flex cursor-pointer items-start gap-2.5 rounded-md border border-border px-3 py-2 has-[:checked]:border-primary has-[:checked]:bg-primary/5 has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60"
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
      <span class="space-y-0.5">
        <span class="block text-sm">{choice.label}</span>
        <span class="block text-xs text-muted-foreground">
          {choice.unavailable ?? choice.detail}
        </span>
      </span>
    </label>
  {/each}
</fieldset>
