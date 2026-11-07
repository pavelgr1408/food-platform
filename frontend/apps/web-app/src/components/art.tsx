import { useState, type ReactNode } from "react";

/**
 * Иллюстрации «Лотка». В контракте пока нет полей с фото (FRONTEND-CONTRACT, пробел про imageUrl),
 * поэтому у каждого блюда и ресторана — фирменный рисунок: плоские цвета, контур ink 3 px.
 * Когда бэкенд начнёт отдавать imageUrl, <Thumb> покажет фото, а рисунок останется запасным.
 */

export type DishKind =
  | "burger" | "roll" | "fries" | "drink" | "sandwich" | "pizza" | "salad" | "dessert" | "coffee" | "soup" | "sushi" | "chicken" | "plate";

const KEYWORDS: [DishKind, RegExp][] = [
  ["burger", /бургер|burger|чизбургер/i],
  ["pizza", /пицц|pizza|маргарит|пепперони/i],
  ["sushi", /суши|сашими|maki|нигири|сяке/i],
  ["chicken", /крылышк|крыло|наггетс|нагет|окорочок|ножк|курин(?!ый суп)/i],
  ["roll", /ролл|шаурм|шаверм|врап|wrap|буррито|тортиль|кесадил|лаваш/i],
  ["fries", /фри|картофел|картошк|закуск/i],
  ["sandwich", /сэндвич|сендвич|бутерброд|панини|тост|хот-?дог|клаб/i],
  ["salad", /салат|боул|bowl|цезарь|греческ/i],
  ["soup", /суп|борщ|лапш|рамен|харчо|бульон/i],
  ["coffee", /кофе|капучино|латте|американо|эспрессо|чай|какао/i],
  ["drink", /лимонад|напит|сок|кола|морс|коктейл|вода|смузи|компот|квас/i],
  ["dessert", /десерт|торт|пирожн|чизкейк|мороженое|круассан|маффин|эклер|пончик|брауни/i],
];

export function dishKind(name: string, category?: string): DishKind {
  for (const [k, re] of KEYWORDS) if (re.test(name)) return k;
  if (category) for (const [k, re] of KEYWORDS) if (re.test(category)) return k;
  return "plate";
}

export function hashOf(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); }
  h ^= h >>> 13; h = Math.imul(h, 0x5bd1e995); h ^= h >>> 15;
  return h >>> 0;
}

const BG = ["#FFE680", "#9BE8C8", "#FFB3AB", "#C9D3FF", "#FFD2A6"] as const;
const INK = "#12131A";
const S = { stroke: INK, strokeWidth: 3, strokeLinejoin: "round", strokeLinecap: "round" } as const;

const SPRITES: Record<DishKind, ReactNode> = {
  burger: (
    <g {...S}>
      <path d="M22 78h76c0 11-8 18-19 18H41c-11 0-19-7-19-18z" fill="#F2A73B" />
      <path d="M20 64c8 0 8 6 15 6s8-6 15-6 8 6 15 6 8-6 15-6 8 6 15 6v-3H20z" fill="#5BC264" />
      <rect x="20" y="62" width="80" height="12" rx="6" fill="#7A3B1E" />
      <path d="M22 56h76l-8 12-10-6-10 8-10-8-10 6-10-6-10 8z" fill="#FFD43B" />
      <path d="M24 52C24 32 40 22 60 22s36 10 36 30z" fill="#F2A73B" />
      <g fill="#FFF3D1" strokeWidth="0"><ellipse cx="46" cy="38" rx="3" ry="1.8" /><ellipse cx="62" cy="31" rx="3" ry="1.8" /><ellipse cx="76" cy="40" rx="3" ry="1.8" /><ellipse cx="58" cy="44" rx="3" ry="1.8" /></g>
    </g>
  ),
  fries: (
    <g {...S}>
      <g fill="#FFD43B">
        <rect x="34" y="22" width="9" height="46" rx="3" transform="rotate(-10 38 45)" />
        <rect x="46" y="16" width="9" height="52" rx="3" transform="rotate(-3 50 42)" />
        <rect x="58" y="18" width="9" height="50" rx="3" transform="rotate(4 62 43)" />
        <rect x="70" y="22" width="9" height="46" rx="3" transform="rotate(12 74 45)" />
        <rect x="52" y="28" width="9" height="40" rx="3" />
      </g>
      <path d="M30 60h60l-7 40H37z" fill="#D62A16" />
      <path d="M44 74h32l-1 8H45z" fill="#FFF" strokeWidth="2" />
    </g>
  ),
  drink: (
    <g {...S}>
      <path d="M66 12l8 26" fill="none" strokeWidth="4" stroke="#D62A16" />
      <path d="M32 36h56l-6 62H38z" fill="#FFF3A6" />
      <path d="M35 58h50l-3 40H38z" fill="#FFD43B" />
      <circle cx="60" cy="46" r="11" fill="#FFE680" /><path d="M60 35v22M49 46h22M52 38l16 16M68 38L52 54" strokeWidth="1.8" fill="none" />
      <ellipse cx="60" cy="36" rx="28" ry="4" fill="#FFF" />
    </g>
  ),
  roll: (
    <g {...S}>
      <path d="M28 78C20 56 34 34 56 30l28 8c10 4 14 24 6 40z" fill="#F4D9A4" />
      <path d="M44 34c-16 10-14 34-6 46l12 6c-8-20-8-36 0-52z" fill="#5BC264" />
      <path d="M56 30l28 8c10 4 14 24 6 40l-16 6" fill="#F7E3BC" />
      <circle cx="58" cy="50" r="5" fill="#D62A16" /><circle cx="70" cy="60" r="4" fill="#D62A16" /><circle cx="62" cy="68" r="4" fill="#FFD43B" />
      <path d="M26 86c14 10 52 12 70-4" fill="none" />
    </g>
  ),
  sandwich: (
    <g {...S}>
      <path d="M18 80L60 24l42 56z" fill="#F2C879" />
      <path d="M28 80l32-42 32 42z" fill="#5BC264" />
      <path d="M36 80l24-30 24 30z" fill="#FFD43B" />
      <path d="M46 80l14-17 14 17z" fill="#D62A16" />
      <path d="M16 80h88v10a4 4 0 0 1-4 4H20a4 4 0 0 1-4-4z" fill="#F2C879" />
    </g>
  ),
  pizza: (
    <g {...S}>
      <path d="M60 100L22 34c24-14 52-14 76 0z" fill="#FFD43B" />
      <path d="M22 34c24-14 52-14 76 0l-4 7c-22-11-46-11-68 0z" fill="#F2A73B" />
      <circle cx="48" cy="52" r="8" fill="#D62A16" /><circle cx="70" cy="56" r="8" fill="#D62A16" /><circle cx="60" cy="76" r="7" fill="#D62A16" />
      <path d="M44 48l1 1M68 53l1 1M58 73l1 1" strokeWidth="2" />
    </g>
  ),
  salad: (
    <g {...S}>
      <path d="M26 54c0-14 12-22 20-18 4-10 24-10 28 0 10-2 20 6 20 18z" fill="#5BC264" />
      <circle cx="48" cy="48" r="7" fill="#D62A16" /><circle cx="72" cy="46" r="6" fill="#FFD43B" /><path d="M56 40l8 12" fill="none" />
      <path d="M20 58h80c0 24-16 38-40 38S20 82 20 58z" fill="#FFF" />
      <path d="M32 70h56" fill="none" strokeWidth="2" />
    </g>
  ),
  dessert: (
    <g {...S}>
      <path d="M30 58h60l-6 38H36z" fill="#D62A16" />
      <path d="M44 58l2 38M60 58v38M76 58l-2 38" strokeWidth="2" fill="none" />
      <path d="M26 58c-2-14 8-20 14-18 2-10 18-14 24-4 10-6 22 2 20 16 6 2 6 8 0 8H30c-6 0-6-6-4-2z" fill="#FFB3C7" />
      <circle cx="60" cy="26" r="7" fill="#D62A16" /><path d="M60 20c0-6 4-8 8-8" fill="none" />
    </g>
  ),
  coffee: (
    <g {...S}>
      <path d="M44 10c-6 8 6 10 0 18M60 8c-6 8 6 10 0 18M76 10c-6 8 6 10 0 18" fill="none" strokeWidth="2.5" />
      <path d="M26 44h58v28c0 16-12 26-29 26S26 88 26 72z" fill="#FFF" />
      <path d="M84 52h8c8 0 10 18-2 22l-6 1" fill="none" />
      <path d="M26 58h58" strokeWidth="2" fill="none" /><path d="M26 44h58" fill="#7A3B1E" />
      <ellipse cx="55" cy="44" rx="29" ry="5" fill="#7A3B1E" />
      <ellipse cx="55" cy="104" rx="38" ry="4" fill="#12131A" opacity=".15" stroke="none" />
    </g>
  ),
  soup: (
    <g {...S}>
      <path d="M40 16c-6 8 6 10 0 18M58 12c-6 8 6 10 0 18M76 16c-6 8 6 10 0 18" fill="none" strokeWidth="2.5" />
      <ellipse cx="60" cy="56" rx="38" ry="9" fill="#F2A73B" />
      <path d="M22 56c0 26 16 40 38 40s38-14 38-40" fill="#FFF" />
      <circle cx="46" cy="55" r="4" fill="#D62A16" /><circle cx="68" cy="58" r="4" fill="#5BC264" /><circle cx="58" cy="52" r="3" fill="#FFD43B" />
    </g>
  ),
  sushi: (
    <g {...S}>
      <ellipse cx="60" cy="84" rx="42" ry="8" fill="#12131A" opacity=".12" stroke="none" />
      <path d="M22 50h76v32c0 6-8 10-38 10s-38-4-38-10z" fill="#FFF" />
      <ellipse cx="60" cy="50" rx="38" ry="12" fill="#FFF" />
      <ellipse cx="60" cy="50" rx="26" ry="7" fill="#FF8A6B" />
      <path d="M36 50c8-5 14-5 24 0M60 50c8-5 14-5 24 0" fill="none" strokeWidth="1.8" />
      <path d="M22 50v10c0 4 4 7 10 8" fill="none" strokeWidth="0" />
      <path d="M22 66h76" strokeWidth="9" stroke="#12131A" fill="none" />
    </g>
  ),
  chicken: (
    <g {...S}>
      <path d="M78 22c14 4 22 18 16 32-4 9-14 14-24 14L52 86c-3 5-10 6-14 2s-3-10 2-14l16-18c-1-14 4-28 22-34z" fill="#F2A73B" transform="rotate(8 60 60)" />
      <path d="M44 74l-14 14" strokeWidth="5" fill="none" stroke="#FFF3D1" />
      <circle cx="26" cy="92" r="6" fill="#FFF" /><circle cx="36" cy="100" r="6" fill="#FFF" />
      <path d="M70 34c6 2 10 6 12 12" fill="none" strokeWidth="2.5" stroke="#FFD43B" />
    </g>
  ),
  plate: (
    <g {...S}>
      <circle cx="60" cy="62" r="40" fill="#FFF" />
      <circle cx="60" cy="62" r="26" fill="#FFF3A6" />
      <path d="M12 20v26c0 4 4 6 6 6v34M18 20v26M24 20v26" fill="none" />
      <path d="M104 20c-8 6-8 26 0 30v34" fill="none" />
    </g>
  ),
};

/** Рисунок блюда на цветной подложке. Подложка стабильна для одного и того же id/названия; рисунок целиком, без обрезки. */
export function DishArt({ name, category, seed, className = "" }: { name: string; category?: string; seed?: string; className?: string }) {
  const kind = dishKind(name, category);
  const bg = BG[hashOf(seed ?? name) % BG.length]!;
  return (
    <div className={`relative overflow-hidden ${className}`} style={{ backgroundColor: bg }}>
      <span aria-hidden className="absolute -right-6 -top-6 size-24 rounded-full bg-white/35" />
      <span aria-hidden className="absolute -bottom-8 -left-6 size-28 rounded-full bg-white/25" />
      <svg viewBox="0 0 120 120" role="img" aria-label={name} className="relative block size-full" preserveAspectRatio="xMidYMid meet">
        <g transform="translate(60 62) scale(.9) translate(-60 -62)">{SPRITES[kind]}</g>
      </svg>
    </div>
  );
}

/** Картинка позиции/ресторана: фото из imageUrl, а если его нет или оно не загрузилось — рисунок. */
export function Thumb({ name, category, seed, imageUrl, className = "" }: { name: string; category?: string; seed?: string; imageUrl?: string; className?: string }) {
  const [broken, setBroken] = useState(false);
  if (imageUrl && !broken) {
    return <img src={imageUrl} alt={name} loading="lazy" decoding="async" onError={() => setBroken(true)} className={`block object-cover ${className}`} />;
  }
  return <DishArt name={name} category={category} seed={seed} className={className} />;
}

/** imageUrl появится в контракте отдельным PR (x-status: planned); читаем безопасно. */
export const imageOf = (x: unknown): string | undefined => {
  const u = (x as { imageUrl?: unknown } | null)?.imageUrl;
  return typeof u === "string" && /^https:\/\//.test(u) ? u : undefined;
};

/** Обложка ресторана: пять рисунков в ряд на цветном фоне, без внешних картинок. Состав и цвет зависят от id. */
export function Cover({ name, seed, className = "" }: { name: string; seed: string; className?: string }) {
  const h = hashOf(seed);
  const all: DishKind[] = ["burger", "fries", "drink", "pizza", "roll", "sandwich", "dessert", "salad", "coffee", "chicken"];
  const step = [1, 3, 7, 9][(h >>> 5) % 4]!;
  const kinds = [0, 1, 2, 3, 4].map((i) => all[((h % all.length) + i * step) % all.length]!);
  const bg = ["#D62A16", "#12131A", "#17A673", "#E8473B", "#4F5DFF"][(h >>> 9) % 5]!;
  return (
    <svg viewBox="0 0 600 140" role="img" aria-label={`Ресторан ${name}`} className={`block ${className}`} preserveAspectRatio="xMidYMid slice">
      <rect width="600" height="140" fill={bg} />
      <circle cx="540" cy="10" r="80" fill="#FFD43B" opacity=".2" />
      <circle cx="40" cy="150" r="70" fill="#fff" opacity=".1" />
      {kinds.map((k, i) => (
        <g key={i} transform={`translate(${12 + i * 116} ${i % 2 ? 22 : 8}) rotate(${[-7, 5, -3, 6, -5][i]} 50 50) scale(.86)`}>
          <circle cx="60" cy="62" r="52" fill="#fff" opacity=".93" />
          {SPRITES[k]}
        </g>
      ))}
    </svg>
  );
}
