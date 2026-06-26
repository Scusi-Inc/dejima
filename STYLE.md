# Dejima writing style

This governs the marketing site and docs: the homepage, guides, comparison
pages, and posts. The goal is simple. Every page should read like one
opinionated engineer wrote it, not like marketing and not like a model.

Read this before writing a page. If you generate a draft with an LLM, paste
the "Banned" list into the prompt so the tells never make it to the page in
the first place.

## Voice

- Write to a peer who already runs agents. Assume they're competent and busy.
- Have an opinion. "Kubernetes is the wrong tool for three agents on a Mac
  mini" beats "Kubernetes may not be ideal for smaller setups."
- Open on something concrete. A real scene, a real command, a real failure.
  Never open on an abstraction ("Managing agents is challenging.").
- The command is the hero. Show it. Don't write a paragraph describing it.
- Make the reader feel smart, not you. Clarity over cleverness. Jargon you
  reach for to sound impressive is the tell of someone who isn't sure.

## Rhythm

- Vary sentence length on purpose. Write a long one that carries the idea,
  then a short one. Then stop. Uniform medium-length sentences read like a
  machine; uneven ones read like a person.
- One idea per paragraph. One or two sentences is fine. White space is good.
- Active voice, second person. "You grant the folder," not "folders can be
  granted."
- Contractions on. "Doesn't," "you'll," "here's."

## Banned (these are the AI tells, reject in review)

Words: delve, leverage, utilize, robust, seamless, showcase, underscore,
pivotal, crucial, comprehensive, nuanced, intricate, testament, tapestry,
realm, boast, meticulous, supercharge, unlock, elevate, foster, streamline,
game-changer, "in today's world", "fast-paced".

Patterns:
- "Not just X, but Y" and "It's not X, it's Y" (negative parallelism).
- Rule-of-three lists where three things are stacked for rhythm, not because
  there are exactly three.
- Dodging plain "is/are" with "serves as", "stands as", "marks", "boasts".
- Participle tails that add nothing: "...streamlining your workflow",
  "...ensuring security", "...making it easy".
- "It's important to note", "in conclusion", "ultimately", "whether you're
  X or Y", "look no further", "the best part".
- Vague attribution: "experts agree", "studies show", "many developers".

Format:
- Sentence case in headings, not Title Case.
- No bold on every keyword. Bold a phrase when it earns it, rarely.
- Em dashes: avoid them. A period or a comma is almost always cleaner, and
  their density is the single most recognizable AI tell. Use a colon when you
  mean "here it comes."
- No horizontal rule dropped in before every heading.
- Don't end a section with a summary sentence that restates it. End on the
  last real point.

## Do

- Use real numbers, real tool names, real file paths.
- Show the failure honestly before you show the fix.
- Cut the wind-up. The first sentence a reader needs is usually the third one
  you wrote. Delete the first two.

## Before / after

Bad:
> Managing multiple agents can quickly become complex, introducing potential
> security risks and reducing visibility into what each agent is doing.

Good:
> Six panes, three agents, and one of them has had your GitHub token for two
> hours. Which one wrote to `~/.ssh`? You can't actually say.

Bad:
> Dejima leverages robust containerization to seamlessly isolate your agents,
> ensuring a secure and streamlined workflow.

Good:
> Each agent runs in its own container. It can't see your files, your keys, or
> the other agents. Host access is off by default; you grant folders one at a
> time, read-only.
