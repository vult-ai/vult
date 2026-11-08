import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";
const legacy = await loadComponent("src/lib/legacy-personal-data.ts");
const user = "a".repeat(64), other = "b".repeat(64);
const preferences = { version:1, language:"en",regional_format:"pl-PL",date_format:"iso",time_format:"24h",digit_grouping:true,negative_style:"parentheses" };
const storage = (entries) => {const values=new Map(entries);return {values,getItem:key=>values.get(key)??null,setItem:(key,value)=>values.set(key,value)};};
test("legacy browser values are an explicit proposal and remain untouched",async()=>{
 const source=storage([["vult-profile:v1:browser",JSON.stringify({owner:{kind:"browser"},profile:{version:1,display_name:"Browser person",photo:null}})],["vult-presentation:v1:browser",JSON.stringify({owner:{kind:"browser"},preferences})],["vult-theme","dark"]]);
 const original=new Map(source.values);const copy=await legacy.readLegacyPersonalData(source);
 assert.equal(copy.patch.profile.display_name,"Browser person");assert.equal(copy.patch.preferences.regional_format,"pl-PL");assert.equal(copy.patch.appearance,"dark");assert.equal("version" in copy.patch.preferences,false);
 assert.deepEqual(source.values,original);assert.match(copy.fingerprint,/^[a-f0-9]{64}$/);
 assert.equal(legacy.legacyChoiceRemembered(source,user,copy),false);legacy.rememberLegacyChoice(source,user,copy);
 assert.equal(legacy.legacyChoiceRemembered(source,user,copy),true);assert.equal(legacy.legacyChoiceRemembered(source,other,copy),false);
 for(const [key,value]of original)assert.equal(source.values.get(key),value);
 source.setItem("vult-theme","light");const changed=await legacy.readLegacyPersonalData(source);assert.notEqual(changed.fingerprint,copy.fingerprint);assert.equal(legacy.legacyChoiceRemembered(source,user,changed),false);
});
test("foreign, corrupt and unsupported browser data never becomes a User profile",async()=>{
 for(const value of ["broken",JSON.stringify({owner:{kind:"user",id:other},profile:{version:1,display_name:"Other user",photo:null}}),JSON.stringify({owner:{kind:"browser"},profile:{version:99,display_name:"Future",photo:null}})]){
  const source=storage([["vult-profile:v1:browser",value]]);const copy=await legacy.readLegacyPersonalData(source);
  assert.equal(copy.warning,true);assert.equal(copy.profile,null);assert.equal(copy.patch.profile,undefined);assert.equal(source.getItem("vult-profile:v1:browser"),value);
 }
 assert.equal(await legacy.readLegacyPersonalData(null),null);
 assert.equal(await legacy.readLegacyPersonalData({getItem(){throw Error("locked")},setItem(){}}),null);
});
