# Creating components

A component is created by extending `HTMLElement`. The name of the component should match the filename. The component should be exported by the file.

```ts
// ElementBreadcrumbs.ts
export class ElementBreadcrumbs extends HTMLElement {
}
```

## Where to put components

If the component is for a specific panel, and not expected to be re-usable, it should be created within the panel's folder, within a sub-directory of `components`:

```
front_end/panels/elements/components/ElementsBreadcrumbs.ts
```

If a component is designed to be re-usable, it should live in `front_end/ui/components`, in its own folder. That folder also contains an entrypoint, along with files for the component's definition.

```
front_end/ui/components/button/button.ts // entrypoint
front_end/ui/components/button/Button.ts // component definition
```

## Defining and naming a component

All custom elements **must** contain a hyphen. Use `devtools-` as the prefix.  Use `customElements.define` to define the component and register it with the browser:

```ts
customElements.define('devtools-elements-breadcrumbs', ElementsBreadcrumbs);
```

And finally we tell TypeScript that this component exists:

```ts
declare global {
  interface HTMLElementTagNameMap {
    'devtools-elements-breadcrumbs': ElementsBreadcrumbs;
  }
}
```

By doing this, TypeScript understands that `document.querySelector('devtools-elements-breadcrumbs')` returns an `ElementsBreadcrumbs` instance.

We use lit-analyzer in a lint state to verify that component definitions are imported where the component is used.

## Creating a shadow root

Each component gets its own Shadow Root to ensure that styles and events that occur within it are encapsulated and do not leak out:

```ts
export class ElementsBreadcrumbs extends HTMLElement {
  readonly #shadow = this.attachShadow({mode: 'open'});
}
```

> We set the mode to `open` so it's open to inspection via DevTools. [See this MDN explainer](https://developer.mozilla.org/en-US/docs/Web/API/ShadowRoot/mode) for more details.

## Rendering a component

Each component defines a `#render` method which is responsible for invoking LitHtml and having the component render HTML into the DOM.

> The `#` symbol indicates a [private class method](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Classes/Private_class_fields).

The `#render` method calls `LitHtml.render`, building up a template with `LitHtml.html`:

```ts
LitHtml.render(html`<p>hello world!</p>`, this.#shadow, {host: this});
```

The third argument (`{host: this}`) tells LitHtml to automatically bind event listeners to the component. This can save you some painful debugging where event listeners do not have the right `this` reference; so we enforce the use of `{host: this}` via a custom ESLint rule.

There is unfortunately a [clang-format bug](crbug.com/1079231) which makes its auto-formatting of LitHtml templates very unreadable, so we usually disable clang-format round the call:

```ts
// clang-format off
LitHtml.render(...)
// clang-format on
```

## Scheduled and batched rendering

To have your component render, you could manually call `this.#render()`. However, if you were to have multiple updates to a component (perhaps some values it's passed get changed), we want to avoid having multiple renders where possible and instead batch them.

We can use the `ScheduledRender` helper (`front_end/ui/components/helpers/scheduled-render.ts`) to achieve this. Rather than call `this.#render()` directly, you instead call the scheduler:

```ts
void ComponentHelpers.ScheduledRender.scheduleRender(this, this.#render);
```

> `scheduleRender` returns a promise; we use the `void` keyword to instruct TypeScript that we are purposefully not using `await` to wait for the promise to resolve. When scheduling a render it's most common to "fire and forget".

To summarise, most components start off life looking like:

```ts
export class ElementsBreadcrumbs extends HTMLElement {
  readonly #shadow = this.attachShadow({mode: 'open'});

  #render(): void {
    LitHtml.render(html``, this.#shadow, {host:this});
  }
}
```

## Triggering a render

One of the most important aspects to understand about our component system is that **rendering does not happen automatically**.

To ensure we trigger a render once the component is added to the DOM, we can define [`connectedCallback`](https://developer.mozilla.org/en-US/docs/Web/Web_Components/Using_custom_elements#:~:text=lifecycle.%20For%20example%2C-,connectedCallback,-is%20invoked%20each):

```ts
export class ElementsBreadcrumbs extends HTMLElement {
  readonly #shadow = this.attachShadow({mode: 'open'});

  connectedCallback(): void {
    void ComponentHelpers.ScheduledRender.scheduleRender(this, this.#render);
  }

  #render(): void {
    LitHtml.render(html``, this.#shadow, {host:this});
  }
}
```

## Passing data into a component

Most of our components will require data that is passed into them. For example, `ElementsBreadcrumbs` takes in an array of `DOMNode` objects.

To provide a component some data, we define a `data` setter. This setter takes an object with any data the component requires. This object should have a TypeScript interface defined for it:

```ts
export interface ElementsBreadcrumbsData {
  selectedNode: DOMNode|null;
  crumbs: DOMNode[];
}

export class ElementsBreadcrumbs extends HTMLElement {
  // ...
  #crumbs: DOMNode[] = [];
  #selectedNode: DOMNode|null = null;

  set data(data: ElementsBreadcrumbsData) {
    this.#crumbs = data.crumbs;
    this.#selectedNode = data.selectedNode;
    void ComponentHelpers.ScheduledRender.scheduleRender(this, this.#render);
  }
}
```

## Rendering components

Now we know how to create components that can take data, let's render them!

### From a DevTools Widget

If you are rendering a component from the DevTools widget system, you should instantiate the component, pass any `data` to it, and then append it into the DOM:

```ts
// Within a Widget
const breadcrumbs = new ElementsBreadcrumbs();
breadcrumbs.data = {selectedNode: node, crumbs: [...]};
this.appendChild(breadcrumbs);
```

### From another component

If you are rendering a component from within another component, import the module defining it and render within the call to `LitHtml.html`:

```ts
// Within some component
LitHtml.render(html`
  <devtools-elements-breadcrumbs></devtools-elements-breadcrumbs>
`, this.#shadow, {host: this});
```

To pass data, we use [LitHtml's dot syntax](https://lit.dev/docs/templates/expressions/#property-expressions) to set the `data` property (and invoke our `set data` setter):

```ts
// Within some component
LitHtml.render(html`
  <devtools-elements-breadcrumbs .data=${{
    selectedNode: node,
    crumbs: [...],
  }}>
  </devtools-elements-breadcrumbs>
`, this.#shadow, {host: this});
```

## Performance concerns with data passing

The approach of `set data(data)` was chosen because:

1. It requires few lines of code.
2. It provides some form of type safety via the `as FooData` check.
3. At the time we didn't have a solution for scheduled and batched renders, and didn't want multiple setters to trigger multiple unnecessary renders.

However, using `set data(data)` does come with some negative performance costs:

1. LitHtml will always think the value has changed, because it's an object. If a component renders twice with `.data=${{name: 'Jack'}}`, Lit will think that the value has changed because it's a new object, even though we can see it's holding the same data.
2. This approach causes these objects to be created (and subsequently garbage collected) on/after each render.

For most components in DevTools, these trade-offs are acceptable, and we prefer the type-safety of `set data` and take the usually imperceivable performance hit. However, in rare circumstances, this performance hit is noticeable. A good example of this is in Performance Insights, where we have to constantly re-render components as the user scrolls through their performance timeline. We noticed that this caused a large amount of garbage collection from all the objects being created per-render and then immediately disposed.

For these situations, we can instead move to an approach where we set properties individually. We still define the interface as before, and then define an individual setter for each property:

```ts
interface ElementsBreadcrumbsData {
  selectedNode: DOMNode|null;
  crumbs: DOMNode[];
}

class ElementsBreadcrumbs extends HTMLElement {
  #crumbs: DOMNode[] = [];
  #selectedNode: DOMNode|null = null;

  set crumbs(crumbs: ElementsBreadcrumbsData['crumbs']) {
    this.#crumbs = crumbs;
    void ComponentHelpers.ScheduledRender.scheduleRender(this, this.#render);
  }

  set selectedNode(selectedNode: ElementsBreadcrumbsData['selectedNode']) {
    this.#selectedNode = selectedNode;
    void ComponentHelpers.ScheduledRender.scheduleRender(this, this.#render);
  }
}
```

Rendering this component within another Lit component would now be done like so:

```ts
// Within some component
LitHtml.render(html`
  <devtools-elements-breadcrumbs .crumbs=${[...]} .selectedNode=${node}>
  </devtools-elements-breadcrumbs>
`, this.#shadow, {host: this});
```

This solution is more performant, but less type-safe as TypeScript has no means of checking those values. This is something we may rectify using the `as` pattern, but for now it's preferred to use the `set data` method by default.



