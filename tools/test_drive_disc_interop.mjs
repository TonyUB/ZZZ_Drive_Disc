import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const ours = require('../web/assets/drive-disc-interop.js');

const importedAt = '2026-07-28T12:00:00.000Z';
const sourcePath = 'export.json';
const scanner = [{
  '序号': 1,
  '名称': '流光咏叹',
  '槽位': 1,
  '品质': 'S',
  '等级': 15,
  '最大等级': 15,
  '主属性': { '生命值': 2200 },
  '副属性': [
    { '攻击力': '6%' },
    { '暴击率': '4.8%' },
    { '暴击伤害': '14.4%' },
    { '攻击力': 38 }
  ]
}];

const parsed = ours.parseImport(scanner, { importedAt, sourcePath });
assert.equal(parsed.kind, 'scanner');
assert.equal(parsed.discs.length, 1);
assert.equal(parsed.nativeDiscs[0].id, 'scanner-47e2f87d6651');
assert.equal(parsed.nativeDiscs[0].setId, 'astral_voice');
assert.equal(parsed.nativeDiscs[0].contentFingerprint, '47e2f87d6651');
assert.equal(parsed.nativeDiscs[0].identityFingerprint, '0210f3f00d0f');
assert.deepEqual(parsed.nativeDiscs[0].mainStat, {
  stat: 'hpFlat', value: 2200, mode: 'flat', label: '生命值', rawValue: 2200
});

const external = ours.createExport(parsed.discs, {
  exportedAt: importedAt,
  sourceAccountLabel: '兼容测试'
});
assert.equal(external.format, 'zzz-calculator-drive-disc-export');
assert.equal(external.version, 1);
const acceptedAgain = ours.parseImport(external, { sourcePath: 'round-trip.json' });
assert.equal(acceptedAgain.discs.length, 1);
assert.equal(acceptedAgain.discs[0].id, 'scanner-47e2f87d6651');
assert.deepEqual(acceptedAgain.nativeDiscs[0].raw, scanner[0]);

external.driveDiscs[0].futureField = { preserved: true };
external.driveDiscs[0].reservedForAgentId = 'agent-a';
external.driveDiscs[0].excludedForAgentIds = ['agent-b'];
external.driveDiscs[0].equippedBy = 'game-agent';
const nativeParsed = ours.parseImport(external, { sourcePath: 'calculator.json' });
const roundTrip = ours.createExport(nativeParsed.discs, { exportedAt: importedAt });
assert.deepEqual(roundTrip.driveDiscs[0].futureField, { preserved: true });
assert.equal(roundTrip.driveDiscs[0].reservedForAgentId, 'agent-a');
assert.deepEqual(roundTrip.driveDiscs[0].excludedForAgentIds, ['agent-b']);
assert.equal(roundTrip.driveDiscs[0].equippedBy, 'game-agent');

const firstMerge = ours.mergeDiscs([], parsed);
assert.deepEqual(firstMerge.summary, { added: 1, updated: 0, skipped: 0, duplicateInImport: 0 });
const secondMerge = ours.mergeDiscs(firstMerge.discs, parsed);
assert.equal(secondMerge.discs.length, 1);
assert.equal(secondMerge.summary.skipped, 1);

const version31Scanner = [{
  '序号': 2,
  '名称': '谶羽之誓',
  '槽位': 5,
  '品质': 'S',
  '等级': 15,
  '最大等级': 15,
  '主属性': { '流明属性伤害加成': '30%' },
  '副属性': [
    { '异常精通': 27 },
    { '攻击力': '6%' },
    { '穿透值': 9 },
    { '防御力': 15 }
  ]
}];
const parsedVersion31 = ours.parseImport(version31Scanner, { importedAt, sourcePath });
assert.equal(parsedVersion31.nativeDiscs[0].setId, '34100');
assert.equal(parsedVersion31.nativeDiscs[0].mainStat.stat, 'lumifluxDmg');
assert.equal(parsedVersion31.discs[0].mainStat.type, 'LUMIFLUX_DMG');
const exportedVersion31 = ours.createExport(parsedVersion31.discs, { exportedAt: importedAt });
assert.equal(exportedVersion31.driveDiscs[0].setId, '34100');
assert.equal(exportedVersion31.driveDiscs[0].mainStat.stat, 'lumifluxDmg');

const rejected = { ...external, format: 'unknown-format' };
assert.throws(() => ours.parseImport(rejected), /不支持的交换格式/);

console.log('drive-disc interoperability tests passed');
