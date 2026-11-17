import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {launchArguments} from './launch.mjs';
const hash = v => createHash('sha256').update(v).digest('hex');
const extension = {path:'/opt/chora/permissions/required.mjs',sha256:hash('required file'),closure:hash('closure'),semantics:hash('same contract')};
const plan = () => ({tools:['read','bash','edit','write'],originalRequired:[extension],installed:[extension],readOnlyPaths:['repo/.git']});
const launch = (p,read=()=>Buffer.from('required file')) => {const raw=JSON.stringify(p);return launchArguments(raw,hash(raw),read);};
test('final argv retains the exact tools and all required extensions',()=>{
 const args=launch(plan());assert.equal(args[args.indexOf('--tools')+1],'read,bash,edit,write');assert.deepEqual(args.slice(-2),['--extension',extension.path]);assert.ok(args.includes('--no-context-files'));
});
test('missing inventory, removed or changed required extension, widened tools and byte drift refuse',()=>{
 for(const change of [p=>delete p.originalRequired,p=>p.installed=[],p=>p.tools.push('network'),p=>p.tools.push('read'),p=>{p.originalRequired=[{...extension,path:'/opt/chora/permissions/sub/a.mjs'}];p.installed=p.originalRequired;}]) {const p=plan();change(p);assert.throws(()=>launch(p));}
 assert.throws(()=>launch(plan(),()=>Buffer.from('changed')));
 assert.throws(()=>launchArguments('{}','0'.repeat(64),()=>Buffer.alloc(0)));
});
