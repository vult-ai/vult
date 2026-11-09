/* The app's icons, from Phosphor (MIT): https://phosphoricons.com.
 * Every component imports its icons from here, by the names it already uses,
 * so the library can change in one place. Icons default to 24 pixels and the
 * bold weight, are hidden from assistive technology unless labelled, and
 * accept Phosphor's `weight` (for example "fill" for a selected state). */
import { forwardRef, type ComponentProps } from "react";
import {
  ArrowDown as PhArrowDown,
  ArrowDownLeft as PhArrowDownLeft,
  ArrowLeft as PhArrowLeft,
  ArrowRight as PhArrowRight,
  Fingerprint as PhFingerprint,
  ArrowUUpLeft as PhArrowUUpLeft,
  ArrowUpRight as PhArrowUpRight,
  ArrowsLeftRight as PhArrowsLeftRight,
  BookOpen as PhBookOpen,
  CalendarBlank as PhCalendarBlank,
  CaretDoubleDown as PhCaretDoubleDown,
  CaretDoubleUp as PhCaretDoubleUp,
  CaretDown as PhCaretDown,
  CaretLeft as PhCaretLeft,
  CaretRight as PhCaretRight,
  CaretUp as PhCaretUp,
  ChartBar as PhChartBar,
  ChartPie as PhChartPie,
  ChartPieSlice as PhChartPieSlice,
  Check as PhCheck,
  CheckCircle as PhCheckCircle,
  Checks as PhChecks,
  Stamp as PhStamp,
  CircleNotch as PhCircleNotch,
  ClipboardText as PhClipboardText,
  Clock as PhClock,
  CloudArrowUp as PhCloudArrowUp,
  Coins as PhCoins,
  Columns as PhColumns,
  Copy as PhCopy,
  CreditCard as PhCreditCard,
  DotsNine as PhDotsNine,
  DotsThree as PhDotsThree,
  DotsThreeVertical as PhDotsThreeVertical,
  DownloadSimple as PhDownloadSimple,
  Eye as PhEye,
  EyeSlash as PhEyeSlash,
  FileArrowDown as PhFileArrowDown,
  FileText as PhFileText,
  FileX as PhFileX,
  Folder as PhFolder,
  FunnelSimple as PhFunnelSimple,
  GearSix as PhGearSix,
  SquaresFour as PhSquaresFour,
  Info as PhInfo,
  Lightbulb as PhLightbulb,
  List as PhList,
  Lock as PhLock,
  MagnifyingGlass as PhMagnifyingGlass,
  Minus as PhMinus,
  Monitor as PhMonitor,
  Moon as PhMoon,
  Paperclip as PhPaperclip,
  Plus as PhPlus,
  SignOut as PhSignOut,
  PlugsConnected as PhPlugsConnected,
  ImageSquare as PhImageSquare,
  LinkSimple as PhLinkSimple,
  IdentificationCard as PhIdentificationCard,
  TextAa as PhTextAa,
  Printer as PhPrinter,
  PushPin as PhPushPin,
  PushPinSimple as PhPushPinSimple,
  Question as PhQuestion,
  Receipt as PhReceipt,
  Scales as PhScales,
  SlidersHorizontal as PhSlidersHorizontal,
  Stack as PhStack,
  Sun as PhSun,
  Tag as PhTag,
  TreeStructure as PhTreeStructure,
  User as PhUser,
  Wallet as PhWallet,
  Warning as PhWarning,
  WarningCircle as PhWarningCircle,
  WarningOctagon as PhWarningOctagon,
  WifiSlash as PhWifiSlash,
  X as PhX,
  type Icon as PhosphorIcon,
} from "@phosphor-icons/react";

export type IconProps = ComponentProps<PhosphorIcon>;
const icon = (Phosphor: PhosphorIcon, name: string) => {
  const Wrapped = forwardRef<SVGSVGElement, IconProps>(function VultIcon(props, ref) {
    return <Phosphor ref={ref} size={24} weight="bold" aria-hidden={props["aria-label"] ? undefined : true} {...props} />;
  });
  Wrapped.displayName = name;
  return Wrapped;
};

export const AlertCircle = icon(PhWarningCircle, "AlertCircle");
export const AlertOctagon = icon(PhWarningOctagon, "AlertOctagon");
export const AlertTriangle = icon(PhWarning, "AlertTriangle");
export const ArrowDown = icon(PhArrowDown, "ArrowDown");
export const ArrowDownLeft = icon(PhArrowDownLeft, "ArrowDownLeft");
export const ArrowLeft = icon(PhArrowLeft, "ArrowLeft");
export const ArrowRight = icon(PhArrowRight, "ArrowRight");
export const Fingerprint = icon(PhFingerprint, "Fingerprint");
export const ArrowUpRight = icon(PhArrowUpRight, "ArrowUpRight");
export const BarChart07 = icon(PhChartBar, "BarChart07");
export const BookOpen01 = icon(PhBookOpen, "BookOpen01");
export const Calendar = icon(PhCalendarBlank, "Calendar");
export const Check = icon(PhCheck, "Check");
export const CheckCircle = icon(PhCheckCircle, "CheckCircle");
export const CheckDone01 = icon(PhChecks, "CheckDone01");
export const Stamp = icon(PhStamp, "Stamp");
export const ChevronDown = icon(PhCaretDown, "ChevronDown");
export const ChevronDownDouble = icon(PhCaretDoubleDown, "ChevronDownDouble");
export const ChevronLeft = icon(PhCaretLeft, "ChevronLeft");
export const ChevronRight = icon(PhCaretRight, "ChevronRight");
export const ChevronUp = icon(PhCaretUp, "ChevronUp");
export const ChevronUpDouble = icon(PhCaretDoubleUp, "ChevronUpDouble");
export const ClipboardCheck = icon(PhClipboardText, "ClipboardCheck");
export const Clock = icon(PhClock, "Clock");
export const Coins01 = icon(PhCoins, "Coins01");
export const Columns02 = icon(PhColumns, "Columns02");
export const Columns03 = icon(PhColumns, "Columns03");
export const Copy01 = icon(PhCopy, "Copy01");
export const CreditCard01 = icon(PhCreditCard, "CreditCard01");
export const CreditCard02 = icon(PhCreditCard, "CreditCard02");
export const Dataflow03 = icon(PhTreeStructure, "Dataflow03");
export const DotsGrid = icon(PhDotsNine, "DotsGrid");
export const DotsHorizontal = icon(PhDotsThree, "DotsHorizontal");
export const DotsVertical = icon(PhDotsThreeVertical, "DotsVertical");
export const Download01 = icon(PhDownloadSimple, "Download01");
export const Eye = icon(PhEye, "Eye");
export const EyeOff = icon(PhEyeSlash, "EyeOff");
export const File06 = icon(PhFileText, "File06");
export const FileDownload02 = icon(PhFileArrowDown, "FileDownload02");
export const FileX02 = icon(PhFileX, "FileX02");
export const FilterLines = icon(PhFunnelSimple, "FilterLines");
export const FlipBackward = icon(PhArrowUUpLeft, "FlipBackward");
export const Folder = icon(PhFolder, "Folder");
export const HelpCircle = icon(PhQuestion, "HelpCircle");
export const SquaresFour = icon(PhSquaresFour, "SquaresFour");
export const InfoCircle = icon(PhInfo, "InfoCircle");
export const LayersThree01 = icon(PhStack, "LayersThree01");
export const Lightbulb02 = icon(PhLightbulb, "Lightbulb02");
export const Loading01 = icon(PhCircleNotch, "Loading01");
export const Lock01 = icon(PhLock, "Lock01");
export const Menu01 = icon(PhList, "Menu01");
export const Minus = icon(PhMinus, "Minus");
export const Monitor01 = icon(PhMonitor, "Monitor01");
export const Moon01 = icon(PhMoon, "Moon01");
export const Paperclip = icon(PhPaperclip, "Paperclip");
export const PieChart02 = icon(PhChartPie, "PieChart02");
export const PieChart03 = icon(PhChartPieSlice, "PieChart03");
export const Pin01 = icon(PhPushPinSimple, "Pin01");
export const Pin02 = icon(PhPushPin, "Pin02");
export const Plus = icon(PhPlus, "Plus");
export const SignOut = icon(PhSignOut, "SignOut");
export const PlugsConnected = icon(PhPlugsConnected, "PlugsConnected");
export const ImageSquare = icon(PhImageSquare, "ImageSquare");
export const LinkSimple = icon(PhLinkSimple, "LinkSimple");
export const IdentificationCard = icon(PhIdentificationCard, "IdentificationCard");
export const TextAa = icon(PhTextAa, "TextAa");
export const Printer = icon(PhPrinter, "Printer");
export const Receipt = icon(PhReceipt, "Receipt");
export const Scales02 = icon(PhScales, "Scales02");
export const SearchLg = icon(PhMagnifyingGlass, "SearchLg");
export const Settings01 = icon(PhGearSix, "Settings01");
export const Sliders01 = icon(PhSlidersHorizontal, "Sliders01");
export const Sun = icon(PhSun, "Sun");
export const SwitchHorizontal01 = icon(PhArrowsLeftRight, "SwitchHorizontal01");
export const Tag01 = icon(PhTag, "Tag01");
export const UploadCloud02 = icon(PhCloudArrowUp, "UploadCloud02");
export const User01 = icon(PhUser, "User01");
export const Wallet01 = icon(PhWallet, "Wallet01");
export const Wallet02 = icon(PhWallet, "Wallet02");
export const WifiOff = icon(PhWifiSlash, "WifiOff");
export const X = icon(PhX, "X");
export const XClose = icon(PhX, "XClose");
