(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  root.ZZZDriveInterop = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const EXPORT_FORMAT = 'zzz-calculator-drive-disc-export';
  const EXPORT_VERSION = 1;

  // Kept byte-for-byte compatible with zzz_calculator's current inventory model.
  const SET_ALIASES = {
    '雪兔梦游仙境': { id: 'zzz_wiki_1907', name: { zhCN: '雪兔梦游仙境' } },
    '囚徒手记': { id: 'zzz_wiki_1906', name: { zhCN: '囚徒手记' } },
    '啄木鸟电音': { id: 'woodpecker_electro', name: { zhCN: '啄木鸟电音' } },
    '摇摆爵士': { id: 'swing_jazz', name: { zhCN: '摇摆爵士' } },
    '激素朋克': { id: 'hormone_punk', name: { zhCN: '激素朋克' } },
    '獠牙重金属': { id: 'fanged_metal', name: { zhCN: '獠牙重金属' } },
    '震星迪斯科': { id: 'shockstar_disco', name: { zhCN: '震星迪斯科' } },
    '雷暴重金属': { id: 'thunder_metal', name: { zhCN: '雷暴重金属' } },
    '极地重金属': { id: 'polar_metal', name: { zhCN: '极地重金属' } },
    '自由蓝调': { id: 'freedom_blues', name: { zhCN: '自由蓝调' } },
    '炎狱重金属': { id: 'inferno_metal', name: { zhCN: '炎狱重金属' } },
    '河豚电音': { id: 'puffer_electro', name: { zhCN: '河豚电音' } },
    '灵魂摇滚': { id: 'soul_rock', name: { zhCN: '灵魂摇滚' } },
    '混沌重金属': { id: 'chaotic_metal', name: { zhCN: '混沌重金属' } },
    '原始朋克': { id: 'proto_punk', name: { zhCN: '原始朋克' } },
    '混沌爵士': { id: 'chaos_jazz', name: { zhCN: '混沌爵士' } },
    '静听嘉音': { id: 'zzz_wiki_1001', name: { zhCN: '静听嘉音' } },
    '沧浪行歌': { id: 'scanner-set-fcf8ae93d798', name: { zhCN: '沧浪行歌' } },
    '拂晓生花': { id: 'zzz_wiki_1552', name: { zhCN: '拂晓生花' } },
    '折枝剑歌': { id: 'scanner-set-48ee0a14625f', name: { zhCN: '折枝剑歌' } },
    '流光咏叹': { id: 'astral_voice', name: { zhCN: '流光咏叹' } },
    '法厄同之歌': { id: 'phaethons_melody', name: { zhCN: '法厄同之歌' } },
    '云岿如我': { id: 'yunkui_tales', name: { zhCN: '云岿如我' } },
    '月光骑士颂': { id: 'moonlight_lullaby', name: { zhCN: '月光骑士颂' } },
    '如影相随': { id: 'shadow_harmony', name: { zhCN: '如影相随' } },
    '山大王': { id: 'king_of_the_summit', name: { zhCN: '山大王' } },
    '呼啸沙龙': { id: 'zzz_wiki_2038', name: { zhCN: '呼啸沙龙' } },
    '拂晓行纪': { id: 'zzz_wiki_2029', name: { zhCN: '拂晓行纪' } },
    '谶羽之誓': { id: '34100', name: { zhCN: '谶羽之誓' } },
    '荆棘玫瑰': { id: '34200', name: { zhCN: '荆棘玫瑰' } }
  };

  const SCANNER_STATS = {
    '生命值': { flat: 'hpFlat', pct: 'hpPct' },
    '攻击力': { flat: 'atkFlat', pct: 'atkPct' },
    '防御力': { flat: 'defFlat', pct: 'defPct' },
    '暴击率': { pct: 'critRate' },
    '暴击伤害': { pct: 'critDmg' },
    '异常精通': { flat: 'anomalyProficiency' },
    '异常掌控': { pct: 'anomalyMastery' },
    '冲击力': { pct: 'impact' },
    '能量自动回复': { pct: 'energyRegen' },
    '穿透值': { flat: 'penFlat' },
    '穿透率': { pct: 'penRatio' },
    '物理伤害加成': { pct: 'physicalDmg' },
    '火属性伤害加成': { pct: 'fireDmg' },
    '冰属性伤害加成': { pct: 'iceDmg' },
    '电属性伤害加成': { pct: 'electricDmg' },
    '以太伤害加成': { pct: 'etherDmg' },
    '风属性伤害加成': { pct: 'windDmg' },
    '流明属性伤害加成': { pct: 'lumifluxDmg' }
  };

  const INTERNAL_TO_EXTERNAL = {
    HP_FLAT: 'hpFlat', HP_PERCENT: 'hpPct', ATK_FLAT: 'atkFlat', ATK_PERCENT: 'atkPct',
    DEF_FLAT: 'defFlat', DEF_PERCENT: 'defPct', CRIT_RATE: 'critRate', CRIT_DMG: 'critDmg',
    ANOMALY_PROFICIENCY: 'anomalyProficiency', ANOMALY_MASTERY: 'anomalyMastery',
    IMPACT: 'impact', ENERGY_REGEN: 'energyRegen', PEN_FLAT: 'penFlat', PEN_RATIO: 'penRatio',
    PHYSICAL_DMG: 'physicalDmg', FIRE_DMG: 'fireDmg', ICE_DMG: 'iceDmg',
    ELECTRIC_DMG: 'electricDmg', ETHER_DMG: 'etherDmg', WIND_DMG: 'windDmg',
    LUMIFLUX_DMG: 'lumifluxDmg'
  };
  const EXTERNAL_TO_INTERNAL = Object.fromEntries(Object.entries(INTERNAL_TO_EXTERNAL).map(([a, b]) => [b, a]));
  const EXTERNAL_LABELS = Object.fromEntries(Object.entries(SCANNER_STATS).flatMap(([label, modes]) =>
    Object.values(modes).map(stat => [stat, label])
  ));
  const PCT_EXTERNAL = new Set(Object.entries(SCANNER_STATS).flatMap(([, modes]) => modes.pct ? [modes.pct] : []));

  function clone(value) { return JSON.parse(JSON.stringify(value)); }
  function nowIso() { return new Date().toISOString(); }
  function stableStringify(value) {
    if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`;
    if (value && typeof value === 'object') {
      return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(',')}}`;
    }
    return JSON.stringify(value);
  }
  function defaultInventoryHash(value) {
    let h1 = 0xdeadbeef;
    let h2 = 0x41c6ce57;
    const text = String(value ?? '');
    for (let index = 0; index < text.length; index += 1) {
      const code = text.charCodeAt(index);
      h1 = Math.imul(h1 ^ code, 2654435761);
      h2 = Math.imul(h2 ^ code, 1597334677);
    }
    h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
    h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
    return `${(h2 >>> 0).toString(16).padStart(8, '0')}${(h1 >>> 0).toString(16).padStart(8, '0')}`.slice(0, 12);
  }
  function normalizedStatValue(value) {
    const number = Number(value ?? 0);
    return Number.isFinite(number) ? Number(number.toFixed(5)) : 0;
  }
  function statFingerprintEntry(stat, includeValue) {
    const entry = { stat: stat?.stat ?? 'unknown', mode: stat?.mode ?? 'unknown' };
    if (includeValue) entry.value = normalizedStatValue(stat?.value);
    return entry;
  }
  function contentFingerprint(disc) {
    return defaultInventoryHash(stableStringify({
      setName: disc?.setName ?? '', partition: Number(disc?.partition ?? 0), rarity: String(disc?.rarity ?? ''),
      level: Number(disc?.level ?? 0), mainStat: statFingerprintEntry(disc?.mainStat, true),
      subStats: (disc?.subStats ?? []).map(stat => statFingerprintEntry(stat, true))
    }));
  }
  function identityFingerprint(disc) {
    return defaultInventoryHash(stableStringify({
      setName: disc?.setName ?? '', partition: Number(disc?.partition ?? 0), rarity: String(disc?.rarity ?? ''),
      mainStat: statFingerprintEntry(disc?.mainStat, false),
      subStats: (disc?.subStats ?? []).map(stat => statFingerprintEntry(stat, false))
    }));
  }

  function assertObject(value, context) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${context} 必须是对象`);
  }
  function finiteNumber(value, context) {
    const number = Number(value);
    if (!Number.isFinite(number)) throw new Error(`${context} 必须是有效数字`);
    return number;
  }
  function parseScannerValue(rawValue) {
    if (typeof rawValue === 'string') {
      const trimmed = rawValue.trim();
      if (trimmed.endsWith('%')) return { value: finiteNumber(trimmed.slice(0, -1), '百分比'), mode: 'pct', rawValue };
      return { value: finiteNumber(trimmed, '词条数值'), mode: 'flat', rawValue };
    }
    if (typeof rawValue === 'number') return { value: finiteNumber(rawValue, '词条数值'), mode: 'flat', rawValue };
    throw new Error(`无法识别词条数值 ${JSON.stringify(rawValue)}`);
  }
  function normalizeScannerStat(rawStat, warnings, context) {
    assertObject(rawStat, context);
    const entries = Object.entries(rawStat);
    if (entries.length !== 1) throw new Error(`${context} 必须且只能包含一个属性`);
    const [label, rawValue] = entries[0];
    const parsed = parseScannerValue(rawValue);
    const stat = SCANNER_STATS[label]?.[parsed.mode] ?? null;
    if (!stat) warnings.push(`${context}：未识别属性“${label}”，数据会保留但不参与当前配装器计算`);
    return { stat: stat ?? 'unknown', value: parsed.value, mode: parsed.mode, label, rawValue };
  }
  function scannerItems(input) {
    if (Array.isArray(input)) return input;
    assertObject(input, '扫描器导出');
    const items = [input.items, input.driveDiscs, input.drive_discs, input.discs, input.data, input.export].find(Array.isArray);
    if (!items) throw new Error('没有找到扫描器的驱动盘数组');
    return items;
  }
  function looksLikeScannerItem(item) {
    return !!item && typeof item === 'object' && ('名称' in item || '槽位' in item || '主属性' in item || '副属性' in item);
  }
  function normalizeScanner(input, options) {
    const importedAt = options.importedAt ?? nowIso();
    const sourcePath = options.sourcePath ?? null;
    const importId = `zzz-scanner-${defaultInventoryHash(`default:${sourcePath ?? ''}:${importedAt}`)}`;
    const warnings = [];
    const discs = scannerItems(input).map((rawItem, index) => {
      assertObject(rawItem, `第 ${index + 1} 个驱动盘`);
      if (!looksLikeScannerItem(rawItem)) throw new Error(`第 ${index + 1} 个项目不是 ZZZ-Scanner.Next 驱动盘记录`);
      const sequence = Number(rawItem['序号'] ?? index + 1);
      const setName = String(rawItem['名称'] ?? '').trim();
      const partition = Number(rawItem['槽位']);
      const rarity = String(rawItem['品质'] ?? '').trim();
      const level = finiteNumber(rawItem['等级'], `第 ${index + 1} 个驱动盘等级`);
      const maxLevel = finiteNumber(rawItem['最大等级'] ?? level, `第 ${index + 1} 个驱动盘最大等级`);
      if (!setName) throw new Error(`第 ${index + 1} 个驱动盘缺少名称`);
      if (!Number.isInteger(partition) || partition < 1 || partition > 6) throw new Error(`第 ${index + 1} 个驱动盘槽位必须为 1-6`);
      if (!rarity) throw new Error(`第 ${index + 1} 个驱动盘缺少品质`);
      if (!Array.isArray(rawItem['副属性'])) throw new Error(`第 ${index + 1} 个驱动盘副属性必须是数组`);
      const setMatch = SET_ALIASES[setName];
      const disc = {
        setId: setMatch?.id ?? `scanner-set-${defaultInventoryHash(setName)}`,
        setName, canonicalSetName: setMatch?.name ?? null, partition, rarity, level, maxLevel,
        locked: false, equippedBy: null, reservedForAgentId: null,
        mainStat: normalizeScannerStat(rawItem['主属性'], warnings, `驱动盘 ${sequence} 主属性`),
        subStats: rawItem['副属性'].map((stat, statIndex) => normalizeScannerStat(stat, warnings, `驱动盘 ${sequence} 副属性 ${statIndex + 1}`)),
        source: { type: 'zzz-scanner', sourcePath, importId, importedAt, sequence, rawIndex: index },
        raw: clone(rawItem)
      };
      disc.contentFingerprint = contentFingerprint(disc);
      disc.identityFingerprint = identityFingerprint(disc);
      disc.id = `scanner-${disc.contentFingerprint}`;
      return disc;
    });
    return { kind: 'scanner', label: 'ZZZ-Scanner.Next 扫描结果', warnings, nativeDiscs: discs };
  }

  function normalizeNativeStat(rawStat, context) {
    assertObject(rawStat, context);
    const stat = String(rawStat.stat ?? '').trim();
    if (!stat) throw new Error(`${context}.stat 不能为空`);
    return { ...clone(rawStat), stat, value: finiteNumber(rawStat.value, `${context}.value`) };
  }
  function normalizeNativeDisc(rawItem, index) {
    const context = `driveDiscs[${index}]`;
    assertObject(rawItem, context);
    const id = String(rawItem.id ?? '').trim();
    const setId = String(rawItem.setId ?? '').trim();
    const setName = String(rawItem.setName ?? '').trim();
    const partition = Number(rawItem.partition);
    const rarity = String(rawItem.rarity ?? '').trim();
    const level = finiteNumber(rawItem.level, `${context}.level`);
    const maxLevel = finiteNumber(rawItem.maxLevel ?? rawItem.level, `${context}.maxLevel`);
    if (!id) throw new Error(`${context}.id 不能为空`);
    if (!setId && !setName) throw new Error(`${context} 至少需要 setId 或 setName`);
    if (!Number.isInteger(partition) || partition < 1 || partition > 6) throw new Error(`${context}.partition 必须为 1-6`);
    if (!rarity) throw new Error(`${context}.rarity 不能为空`);
    if (!Array.isArray(rawItem.subStats)) throw new Error(`${context}.subStats 必须是数组`);
    const { ownerId: _ownerId, contentFingerprint: _content, identityFingerprint: _identity, ...record } = clone(rawItem);
    const disc = {
      ...record, id, setId, setName, partition, rarity, level, maxLevel,
      mainStat: normalizeNativeStat(rawItem.mainStat, `${context}.mainStat`),
      subStats: rawItem.subStats.map((stat, statIndex) => normalizeNativeStat(stat, `${context}.subStats[${statIndex}]`))
    };
    disc.contentFingerprint = contentFingerprint(disc);
    disc.identityFingerprint = identityFingerprint(disc);
    return disc;
  }
  function normalizeNativeExport(input) {
    assertObject(input, '驱动盘交换文件');
    if (input.format !== EXPORT_FORMAT) throw new Error(`不支持的交换格式“${input.format}”`);
    if (input.version !== EXPORT_VERSION) throw new Error(`不支持的交换格式版本 ${input.version}，当前只支持 ${EXPORT_VERSION}`);
    assertObject(input.sourceAccount, 'sourceAccount');
    if (!String(input.sourceAccount.label ?? '').trim()) throw new Error('sourceAccount.label 不能为空');
    if (!String(input.exportedAt ?? '').trim() || !Number.isFinite(Date.parse(input.exportedAt))) throw new Error('exportedAt 必须是有效时间');
    if (!Array.isArray(input.driveDiscs)) throw new Error('driveDiscs 必须是数组');
    const nativeDiscs = input.driveDiscs.map(normalizeNativeDisc);
    const ids = new Set();
    for (const disc of nativeDiscs) {
      if (ids.has(disc.id)) throw new Error(`交换文件包含重复 id“${disc.id}”`);
      ids.add(disc.id);
    }
    return { kind: 'native', label: 'zzz_calculator 通用驱动盘文件', warnings: [], nativeDiscs };
  }

  function inferExternalMode(stat) { return PCT_EXTERNAL.has(stat) ? 'pct' : 'flat'; }
  function nativeStatToInternal(stat) {
    const type = EXTERNAL_TO_INTERNAL[stat.stat] ?? '';
    const raw = typeof stat.rawValue === 'string' ? stat.rawValue : '';
    return { ...clone(stat), type, value: Number(stat.value), ...(raw ? { raw } : {}) };
  }
  function nativeDiscToInternal(disc) {
    const { partition, ownerId: _ownerId, contentFingerprint: _content, identityFingerprint: _identity, ...rest } = clone(disc);
    const mainStat = nativeStatToInternal(disc.mainStat);
    const subStats = disc.subStats.map(nativeStatToInternal);
    const now = nowIso();
    return {
      ...rest, id: disc.id, setName: disc.setName, slot: partition, rarity: disc.rarity, level: Number(disc.level),
      stats: [mainStat, ...subStats], mainStat, subStats,
      locked: Boolean(disc.locked), discarded: Boolean(disc.discarded),
      // equippedBy in zzz_calculator is not the same thing as this app's local
      // build claim. Keep it hidden so local claim syncing cannot overwrite it.
      interopEquippedBy: Object.prototype.hasOwnProperty.call(disc, 'equippedBy') ? clone(disc.equippedBy) : null,
      equippedBy: '', note: String(disc.note ?? ''),
      createdAt: disc.createdAt ?? now, updatedAt: disc.updatedAt ?? now
    };
  }
  function internalStatToNative(stat) {
    const { type: _type, raw: _raw, suspect: _suspect, ...rest } = clone(stat ?? {});
    const externalStat = String(stat?.stat ?? INTERNAL_TO_EXTERNAL[stat?.type] ?? '').trim();
    if (!externalStat) throw new Error(`无法把属性类型“${stat?.type ?? ''}”转换为通用格式`);
    const value = finiteNumber(stat?.value, `${externalStat}.value`);
    const mode = stat?.mode ?? inferExternalMode(externalStat);
    const label = stat?.label ?? EXTERNAL_LABELS[externalStat];
    let rawValue = Object.prototype.hasOwnProperty.call(stat ?? {}, 'rawValue') ? stat.rawValue : undefined;
    if (rawValue === undefined) rawValue = mode === 'pct' ? `${value}%` : value;
    return { ...rest, stat: externalStat, value, mode, ...(label ? { label } : {}), rawValue };
  }
  function internalDiscToNative(disc) {
    const {
      slot: _slot, stats: _stats, mainStat: _main, subStats: _subs,
      contentFingerprint: _content, identityFingerprint: _identity, ownerId: _ownerId,
      equippedBy: _localEquippedBy, interopEquippedBy: _interopEquippedBy,
      ...rest
    } = clone(disc ?? {});
    const setName = String(disc?.setName ?? '').trim();
    const setMatch = SET_ALIASES[setName];
    const id = String(disc?.id ?? '').trim();
    const partition = Number(disc?.slot);
    if (!id) throw new Error('驱动盘 id 不能为空');
    if (!setName && !disc?.setId) throw new Error(`驱动盘 ${id} 缺少套装名`);
    if (!Number.isInteger(partition) || partition < 1 || partition > 6) throw new Error(`驱动盘 ${id} 槽位必须为 1-6`);
    return {
      ...rest, id, setId: disc.setId || setMatch?.id || `scanner-set-${defaultInventoryHash(setName)}`,
      setName, ...(disc.canonicalSetName !== undefined ? {} : { canonicalSetName: setMatch?.name ?? null }),
      partition, rarity: String(disc.rarity ?? 'S'), level: Number(disc.level ?? 0),
      maxLevel: Number(disc.maxLevel ?? disc.level ?? 0),
      equippedBy: Object.prototype.hasOwnProperty.call(disc ?? {}, 'interopEquippedBy') ? clone(disc.interopEquippedBy) : null,
      mainStat: internalStatToNative(disc.mainStat),
      subStats: (disc.subStats ?? []).map(internalStatToNative)
    };
  }
  function createExport(discs, options) {
    return {
      format: EXPORT_FORMAT,
      version: EXPORT_VERSION,
      exportedAt: options?.exportedAt ?? nowIso(),
      sourceAccount: { label: String(options?.sourceAccountLabel ?? 'ZZZ Drive Optimizer') },
      driveDiscs: (discs ?? []).map(internalDiscToNative)
    };
  }

  function looksLikeAppState(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input) || !Array.isArray(input.discs)) return false;
    return ['setEffects', 'characterBuilds', 'discClaims', 'claimsInitialized'].some(key => Object.prototype.hasOwnProperty.call(input, key));
  }
  function looksLikeLegacyInternalArray(input) {
    return Array.isArray(input) && input.length > 0 && input.every(item => item && typeof item === 'object' && ('slot' in item || 'setName' in item) && !looksLikeScannerItem(item));
  }
  function looksLikeLooseNativeItems(input) {
    return Array.isArray(input) && input.every(item => item && typeof item === 'object' && 'partition' in item && item.mainStat && 'stat' in item.mainStat);
  }
  function parseImport(input, options) {
    if (input && typeof input === 'object' && !Array.isArray(input) && Object.prototype.hasOwnProperty.call(input, 'format')) {
      const normalized = normalizeNativeExport(input);
      return { ...normalized, discs: normalized.nativeDiscs.map(nativeDiscToInternal) };
    }
    if (looksLikeAppState(input)) return { kind: 'backup', label: '配装器完整备份', warnings: [], state: clone(input), discs: clone(input.discs) };
    if (looksLikeLegacyInternalArray(input)) return { kind: 'backup-array', label: '旧版配装器库存数组', warnings: [], discs: clone(input) };
    const candidates = !Array.isArray(input) && input && typeof input === 'object'
      ? [input.driveDiscs, input.drive_discs, input.items, input.discs, input.data, input.export].find(Array.isArray)
      : input;
    if (looksLikeLooseNativeItems(candidates)) {
      const nativeDiscs = candidates.map(normalizeNativeDisc);
      return { kind: 'native-items', label: 'zzz_calculator 驱动盘数据', warnings: ['文件不是标准交换外壳，已按计算器驱动盘数组导入'], nativeDiscs, discs: nativeDiscs.map(nativeDiscToInternal) };
    }
    const normalized = normalizeScanner(input, options ?? {});
    return { ...normalized, discs: normalized.nativeDiscs.map(nativeDiscToInternal) };
  }

  function comparableNative(disc) {
    const { contentFingerprint: _content, identityFingerprint: _identity, ownerId: _owner, ...rest } = disc;
    return stableStringify(rest);
  }
  function mergeDiscs(currentDiscs, parsed) {
    const current = (currentDiscs ?? []).map(clone);
    const next = new Map(current.map(disc => [String(disc.id), disc]));
    const currentNative = current.map(disc => {
      const native = internalDiscToNative(disc);
      native.contentFingerprint = contentFingerprint(native);
      native.identityFingerprint = identityFingerprint(native);
      return { internal: disc, native };
    });
    const byContent = new Map();
    const byIdentity = new Map();
    for (const item of currentNative) {
      const contents = byContent.get(item.native.contentFingerprint) ?? [];
      contents.push(item); byContent.set(item.native.contentFingerprint, contents);
      const identities = byIdentity.get(item.native.identityFingerprint) ?? [];
      identities.push(item); byIdentity.set(item.native.identityFingerprint, identities);
    }
    const summary = { added: 0, updated: 0, skipped: 0, duplicateInImport: 0 };
    const seenContent = new Set();
    const nativeKind = parsed.kind === 'native' || parsed.kind === 'native-items';
    for (let index = 0; index < parsed.discs.length; index += 1) {
      const importedInternal = clone(parsed.discs[index]);
      const importedNative = internalDiscToNative(importedInternal);
      importedNative.contentFingerprint = contentFingerprint(importedNative);
      importedNative.identityFingerprint = identityFingerprint(importedNative);
      if (!nativeKind && seenContent.has(importedNative.contentFingerprint)) { summary.duplicateInImport += 1; continue; }
      seenContent.add(importedNative.contentFingerprint);
      if (nativeKind && next.has(importedInternal.id)) {
        const existing = next.get(importedInternal.id);
        const before = comparableNative(internalDiscToNative(existing));
        const merged = {
          ...existing, ...importedInternal, id: existing.id,
          discarded: existing.discarded ?? importedInternal.discarded,
          note: existing.note ?? importedInternal.note,
          equippedBy: existing.equippedBy ?? '',
          createdAt: existing.createdAt ?? importedInternal.createdAt
        };
        next.set(existing.id, merged);
        comparableNative(internalDiscToNative(merged)) === before ? summary.skipped++ : summary.updated++;
        continue;
      }
      const contentMatches = byContent.get(importedNative.contentFingerprint) ?? [];
      if (contentMatches.length) {
        const match = contentMatches.find(item => next.has(item.internal.id)) ?? contentMatches[0];
        const existing = next.get(match.internal.id) ?? match.internal;
        next.set(existing.id, {
          ...existing, ...importedInternal, id: existing.id,
          locked: existing.locked ?? importedInternal.locked,
          discarded: existing.discarded ?? importedInternal.discarded,
          note: existing.note ?? importedInternal.note,
          equippedBy: existing.equippedBy ?? '',
          interopEquippedBy: existing.interopEquippedBy ?? importedInternal.interopEquippedBy,
          createdAt: existing.createdAt ?? importedInternal.createdAt
        });
        summary.skipped += 1;
        continue;
      }
      const identityMatches = byIdentity.get(importedNative.identityFingerprint) ?? [];
      if (!nativeKind && identityMatches.length === 1 && Number(importedInternal.level) > Number(identityMatches[0].internal.level)) {
        const existing = next.get(identityMatches[0].internal.id);
        next.set(existing.id, {
          ...existing, ...importedInternal, id: existing.id,
          locked: existing.locked ?? importedInternal.locked,
          discarded: existing.discarded ?? importedInternal.discarded,
          note: existing.note ?? importedInternal.note,
          equippedBy: existing.equippedBy ?? '',
          interopEquippedBy: existing.interopEquippedBy ?? importedInternal.interopEquippedBy,
          createdAt: existing.createdAt
        });
        summary.updated += 1;
        continue;
      }
      let nextId = importedInternal.id;
      while (next.has(nextId)) nextId = `scanner-${importedNative.contentFingerprint}-${defaultInventoryHash(`${nextId}:${next.size}`)}`;
      importedInternal.id = nextId;
      next.set(nextId, importedInternal);
      summary.added += 1;
    }
    return { discs: [...next.values()], summary };
  }

  return {
    EXPORT_FORMAT, EXPORT_VERSION, SET_ALIASES, INTERNAL_TO_EXTERNAL, EXTERNAL_TO_INTERNAL,
    defaultInventoryHash, contentFingerprint, identityFingerprint,
    parseImport, createExport, internalDiscToNative, nativeDiscToInternal, mergeDiscs
  };
});
