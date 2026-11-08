/** Currencies a new Book can keep, matching the ISO 4217 table the ledger
 * records. A Book's currency can't change after it is created. */

const common = ["USD", "EUR", "GBP", "CAD", "AUD", "NZD", "CHF", "JPY", "SGD", "HKD", "INR", "MXN", "BRL", "SEK", "NOK", "DKK", "PLN", "ZAR"];

const euro = new Set(["AT", "BE", "HR", "CY", "EE", "FI", "FR", "DE", "GR", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PT", "SK", "SI", "ES"]);
const byRegion: Record<string, string> = {
  US: "USD", GB: "GBP", CA: "CAD", AU: "AUD", NZ: "NZD", CH: "CHF", JP: "JPY", SG: "SGD", HK: "HKD", IN: "INR",
  MX: "MXN", BR: "BRL", SE: "SEK", NO: "NOK", DK: "DKK", PL: "PLN", ZA: "ZAR", KR: "KRW", CN: "CNY", AE: "AED",
};

/** Minor units for currencies that don't use two decimal places. */
const minorUnits: Record<string, number> = {
  BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
  BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0, KMF: 0, KRW: 0, PYG: 0, RWF: 0, UGX: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,
};

const names: Record<string, string> = {
  AED: "United Arab Emirates Dirham",
  AFN: "Afghan Afghani",
  ALL: "Albanian Lek",
  AMD: "Armenian Dram",
  AOA: "Angolan Kwanza",
  ARS: "Argentine Peso",
  AUD: "Australian Dollar",
  AWG: "Aruban Florin",
  AZN: "Azerbaijani Manat",
  BAM: "Bosnia-Herzegovina Convertible Mark",
  BBD: "Barbadian Dollar",
  BDT: "Bangladeshi Taka",
  BHD: "Bahraini Dinar",
  BIF: "Burundian Franc",
  BMD: "Bermudan Dollar",
  BND: "Brunei Dollar",
  BOB: "Bolivian Boliviano",
  BRL: "Brazilian Real",
  BSD: "Bahamian Dollar",
  BTN: "Bhutanese Ngultrum",
  BWP: "Botswanan Pula",
  BYN: "Belarusian Ruble",
  BZD: "Belize Dollar",
  CAD: "Canadian Dollar",
  CDF: "Congolese Franc",
  CHF: "Swiss Franc",
  CLP: "Chilean Peso",
  CNY: "Chinese Yuan",
  COP: "Colombian Peso",
  CRC: "Costa Rican Colón",
  CUP: "Cuban Peso",
  CVE: "Cape Verdean Escudo",
  CZK: "Czech Koruna",
  DJF: "Djiboutian Franc",
  DKK: "Danish Krone",
  DOP: "Dominican Peso",
  DZD: "Algerian Dinar",
  EGP: "Egyptian Pound",
  ERN: "Eritrean Nakfa",
  ETB: "Ethiopian Birr",
  EUR: "Euro",
  FJD: "Fijian Dollar",
  FKP: "Falkland Islands Pound",
  GBP: "British Pound",
  GEL: "Georgian Lari",
  GHS: "Ghanaian Cedi",
  GIP: "Gibraltar Pound",
  GMD: "Gambian Dalasi",
  GNF: "Guinean Franc",
  GTQ: "Guatemalan Quetzal",
  GYD: "Guyanaese Dollar",
  HKD: "Hong Kong Dollar",
  HNL: "Honduran Lempira",
  HTG: "Haitian Gourde",
  HUF: "Hungarian Forint",
  IDR: "Indonesian Rupiah",
  ILS: "Israeli New Shekel",
  INR: "Indian Rupee",
  IQD: "Iraqi Dinar",
  IRR: "Iranian Rial",
  ISK: "Icelandic Króna",
  JMD: "Jamaican Dollar",
  JOD: "Jordanian Dinar",
  JPY: "Japanese Yen",
  KES: "Kenyan Shilling",
  KGS: "Kyrgyz Som",
  KHR: "Cambodian Riel",
  KMF: "Comorian Franc",
  KPW: "North Korean Won",
  KRW: "South Korean Won",
  KWD: "Kuwaiti Dinar",
  KYD: "Cayman Islands Dollar",
  KZT: "Kazakhstani Tenge",
  LAK: "Laotian Kip",
  LBP: "Lebanese Pound",
  LKR: "Sri Lankan Rupee",
  LRD: "Liberian Dollar",
  LSL: "Lesotho Loti",
  LYD: "Libyan Dinar",
  MAD: "Moroccan Dirham",
  MDL: "Moldovan Leu",
  MGA: "Malagasy Ariary",
  MKD: "Macedonian Denar",
  MMK: "Myanmar Kyat",
  MNT: "Mongolian Tugrik",
  MOP: "Macanese Pataca",
  MRU: "Mauritanian Ouguiya",
  MUR: "Mauritian Rupee",
  MVR: "Maldivian Rufiyaa",
  MWK: "Malawian Kwacha",
  MXN: "Mexican Peso",
  MYR: "Malaysian Ringgit",
  MZN: "Mozambican Metical",
  NAD: "Namibian Dollar",
  NGN: "Nigerian Naira",
  NIO: "Nicaraguan Córdoba",
  NOK: "Norwegian Krone",
  NPR: "Nepalese Rupee",
  NZD: "New Zealand Dollar",
  OMR: "Omani Rial",
  PAB: "Panamanian Balboa",
  PEN: "Peruvian Sol",
  PGK: "Papua New Guinean Kina",
  PHP: "Philippine Peso",
  PKR: "Pakistani Rupee",
  PLN: "Polish Zloty",
  PYG: "Paraguayan Guarani",
  QAR: "Qatari Riyal",
  RON: "Romanian Leu",
  RSD: "Serbian Dinar",
  RUB: "Russian Ruble",
  RWF: "Rwandan Franc",
  SAR: "Saudi Riyal",
  SBD: "Solomon Islands Dollar",
  SCR: "Seychellois Rupee",
  SDG: "Sudanese Pound",
  SEK: "Swedish Krona",
  SGD: "Singapore Dollar",
  SHP: "St. Helena Pound",
  SLE: "Sierra Leonean Leone",
  SOS: "Somali Shilling",
  SRD: "Surinamese Dollar",
  SSP: "South Sudanese Pound",
  STN: "São Tomé & Príncipe Dobra",
  SVC: "Salvadoran Colón",
  SYP: "Syrian Pound",
  SZL: "Swazi Lilangeni",
  THB: "Thai Baht",
  TJS: "Tajikistani Somoni",
  TMT: "Turkmenistani Manat",
  TND: "Tunisian Dinar",
  TOP: "Tongan Paʻanga",
  TRY: "Turkish Lira",
  TTD: "Trinidad & Tobago Dollar",
  TWD: "New Taiwan Dollar",
  TZS: "Tanzanian Shilling",
  UAH: "Ukrainian Hryvnia",
  UGX: "Ugandan Shilling",
  USD: "US Dollar",
  UYU: "Uruguayan Peso",
  UZS: "Uzbekistani Som",
  VED: "Bolívar Soberano",
  VES: "Venezuelan Bolívar",
  VND: "Vietnamese Dong",
  VUV: "Vanuatu Vatu",
  WST: "Samoan Tala",
  XAF: "Central African CFA Franc",
  XCD: "East Caribbean Dollar",
  XCG: "Caribbean guilder",
  XOF: "West African CFA Franc",
  XPF: "CFP Franc",
  YER: "Yemeni Rial",
  ZAR: "South African Rand",
  ZMW: "Zambian Kwacha",
  ZWG: "Zimbabwean Gold",
};

export function currencyLabel(code: string) {
  return names[code] ? `${names[code]} (${code})` : code;
}

/** Decimal places the ledger keeps for a currency. */
export function currencyScale(code: string) {
  return minorUnits[code] ?? 2;
}

export function bookCurrencyOptions(): { label: string; value: string }[] {
  const rest = Object.keys(names)
    .filter((code) => !common.includes(code))
    .sort((a, b) => names[a].localeCompare(names[b], "en"));
  return [...common, ...rest].map((code) => ({ value: code, label: currencyLabel(code) }));
}

/** The currency of the browser's region, when Vult knows it; otherwise US dollars. */
export function browserBookCurrency(languages: readonly string[] = typeof navigator === "undefined" ? [] : navigator.languages): string {
  for (const language of languages) {
    try {
      const region = new Intl.Locale(language).maximize().region;
      if (!region) continue;
      if (euro.has(region)) return "EUR";
      if (byRegion[region]) return byRegion[region];
    } catch {
      /* Try the next language. */
    }
  }
  return "USD";
}
