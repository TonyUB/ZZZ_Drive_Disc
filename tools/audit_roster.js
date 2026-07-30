const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '..');
const index = fs.readFileSync(path.join(root, 'web', 'index.html'), 'utf8');
const assetMap = fs.readFileSync(path.join(root, 'web', 'assets', 'asset-map.js'), 'utf8');

const characterMatch = index.match(/const CHARACTERS\s*=\s*(\[[\s\S]*?\]);([\s\S]*?)const WENGINE_BASE_ATK/);
if (!characterMatch) throw new Error('CHARACTERS array not found');
const characters = Function(`"use strict"; const CHARACTERS = ${characterMatch[1]}; ${characterMatch[2]}; return CHARACTERS;`)();

const roleNames = {
  ATTACK: '强攻',
  RUPTURE: '命破',
  ANOMALY: '异常',
  STUN: '击破',
  DEFENSE: '防护',
  SUPPORT: '支援',
};

const expectedByRole = {
  ATTACK: ['佩洛伊斯','叶瞬光','奥菲丝&「鬼火」','「席德」','雨果','零号·安比','伊芙琳','悠真','朱鸢','「11号」','艾莲','猫又','可琳','安东','比利','希希芙'],
  RUPTURE: ['般岳','伊德海莉','仪玄','真斗','星徽·比利'],
  ANOMALY: ['蕾米埃尔','维琳娜','爱芮','爱丽丝','薇薇安','星见雅','柳','柏妮思','简','格莉丝','派派','普罗米娅'],
  STUN: ['南宫羽','琉音','橘福福','「扳机」','波可娜','诺姆','青衣','莱特','莱卡恩','珂蕾妲','安比'],
  DEFENSE: ['凯撒','赛斯','本','潘引壶','照'],
  SUPPORT: ['千夏','卢西娅','柚叶','耀嘉音','丽娜','苍角','露西','妮可'],
};
const expectedRoleByName = new Map(Object.entries(expectedByRole).flatMap(([role, names]) => names.map(name => [name, role])));
const allowedAvatarFallback = new Set(['蕾米埃尔']);

const byName = new Map();
for (const character of characters) {
  const rows = byName.get(character.name) || [];
  rows.push(character.role);
  byName.set(character.name, rows);
}

const assetNames = [...assetMap.matchAll(/^\s*'([^']+)'\s*:\s*'\/assets\/agents\/agent-\d+\.png'/gm)].map(match => match[1]);
const engineCharacters = [...index.matchAll(/(?:"character"\s*:\s*"([^"]+)"|character\s*:\s*'([^']+)')/g)].map(match => match[1] || match[2]);
const releaseOrderBlock = index.match(/const RELEASE_ORDER_BY_NAME\s*=\s*\{([\s\S]*?)\};/);
const releaseNames = releaseOrderBlock ? [...releaseOrderBlock[1].matchAll(/'([^']+)'\s*:/g)].map(match => match[1]) : [];
const releaseOrder = releaseOrderBlock ? Function(`"use strict"; return ({${releaseOrderBlock[1]}});`)() : {};
const builtInSetMatch = index.match(/const BUILTIN_SET_NAMES\s*=\s*(\[[^;]+\]);/);
const builtInSets = builtInSetMatch ? Function(`"use strict"; return ${builtInSetMatch[1]};`)() : [];
const characterNames = [...byName.keys()];
const roleCounts = Object.fromEntries(Object.keys(roleNames).map(role => [roleNames[role], characters.filter(c => c.role === role).length]));
const unexpectedCharacters = characterNames.filter(name => !expectedRoleByName.has(name));
const missingExpectedCharacters = [...expectedRoleByName.keys()].filter(name => !byName.has(name));
const roleMismatches = characters
  .filter(character => expectedRoleByName.has(character.name) && expectedRoleByName.get(character.name) !== character.role)
  .map(character => ({name: character.name, actual: roleNames[character.role] || character.role, expected: roleNames[expectedRoleByName.get(character.name)]}));
const missingRequiredData = characters
  .filter(character => !['hp', 'atk', 'def'].every(field => Number.isFinite(Number(character[field])) && Number(character[field]) > 0))
  .map(character => character.name);
const releaseSortedCharacters = characters.slice().sort((a, b) => (releaseOrder[b.name] || 0) - (releaseOrder[a.name] || 0) || a.name.localeCompare(b.name, 'zh-CN'));
const roleReleaseOrder = Object.keys(roleNames).map(role => ({
  role,
  label: roleNames[role],
  order: characters.filter(character => character.role === role).reduce((best, character) => Math.max(best, releaseOrder[character.name] || 0), 0),
})).sort((a, b) => b.order - a.order || a.label.localeCompare(b.label, 'zh-CN'));
const latestDriveSets = builtInSets.slice().reverse();
const releaseOrderingErrors = [];
if (releaseSortedCharacters[0]?.name !== '蕾米埃尔') releaseOrderingErrors.push(`最新代理人应为蕾米埃尔，实际为${releaseSortedCharacters[0]?.name || '空'}`);
if (roleReleaseOrder[0]?.role !== 'ANOMALY') releaseOrderingErrors.push(`默认最新职业应为异常，实际为${roleReleaseOrder[0]?.label || '空'}`);
if (latestDriveSets[0] !== '荆棘玫瑰' || latestDriveSets[1] !== '谶羽之誓') releaseOrderingErrors.push(`最新驱动盘顺序错误：${latestDriveSets.slice(0, 2).join('、')}`);
if (!index.includes("function sortSetNames(names){return Array.from(names).sort((a,b)=>releaseOrderOfDriveSet(b)-releaseOrderOfDriveSet(a)")) releaseOrderingErrors.push('套装下拉未使用版本倒序函数');
if (!index.includes('fillRoleControl({selectNewest:true});')) releaseOrderingErrors.push('初始/清空流程未选择最新职业');

const report = {
  records: characters.length,
  uniqueCharacters: characterNames.length,
  roleCounts,
  duplicateRoles: Object.fromEntries([...byName].filter(([, roles]) => roles.length > 1)),
  roleMismatches,
  missingExpectedCharacters,
  unexpectedCharacters,
  missingRequiredData,
  missingAvatar: characterNames.filter(name => !assetNames.includes(name) && !allowedAvatarFallback.has(name)),
  fallbackAvatar: characterNames.filter(name => !assetNames.includes(name) && allowedAvatarFallback.has(name)),
  orphanAvatar: assetNames.filter(name => !characterNames.includes(name)),
  missingWEngine: characterNames.filter(name => !engineCharacters.includes(name)),
  missingReleaseOrder: characterNames.filter(name => !releaseNames.includes(name)),
  latestCharacter: releaseSortedCharacters[0]?.name || '',
  roleReleaseOrder,
  latestDriveSets: latestDriveSets.slice(0, 6),
  releaseOrderingErrors,
  roster: characters.map(character => ({
    name: character.name,
    role: roleNames[character.role] || character.role,
    rank: character.rank || 'S',
    element: character.element || '',
    faction: character.faction || '',
    hp: character.hp,
    atk: character.atk,
    def: character.def,
    impact: character.impact,
    anomalyMastery: character.baseAnomalyMastery,
    anomalyProficiency: character.baseAnomalyProficiency,
    energyRegen: character.baseEnergyRegen,
    core: character.extra || {},
  })),
};

process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
const blockingKeys = ['roleMismatches', 'missingExpectedCharacters', 'unexpectedCharacters', 'missingRequiredData', 'missingAvatar', 'orphanAvatar', 'missingWEngine', 'missingReleaseOrder', 'releaseOrderingErrors'];
if (characters.length !== expectedRoleByName.size || characterNames.length !== expectedRoleByName.size || Object.keys(report.duplicateRoles).length || blockingKeys.some(key => report[key].length)) {
  process.exitCode = 1;
}
