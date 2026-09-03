# Animation Remotion de Quivr V2

Ce projet génère les visuels d’introduction du README à partir de composants React déterministes.

## Compositions

- `QuivrExplainer` — film complet de 25 secondes en 1920 × 1080 à 30 fps ;
- `QuivrReadmeLoop` — boucle de 12 secondes en 1200 × 675, rendue à 10 fps pour le README.

## Développement

```bash
cd video
pnpm install
pnpm dev
```

## Vérification et rendu

```bash
pnpm typecheck
pnpm render:poster
pnpm render
pnpm render:loop
```

Les sorties sont écrites dans `docs/assets/` :

- `quivr-v2-explainer.mp4` ;
- `quivr-v2-explainer-loop.gif` ;
- `quivr-v2-explainer-poster.png`.

Toutes les dépendances Remotion sont verrouillées sur la même version afin de garantir un rendu reproductible.
