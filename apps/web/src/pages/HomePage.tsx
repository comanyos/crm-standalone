import { lazy } from "react";
import { Navigate } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";

const DashboardPage = lazy(() => import("./DashboardPage"));

export default function HomePage() {
  const { hasRole } = useAuth();
  return hasRole("platform_admin") ? <Navigate to="/admin" replace /> : <DashboardPage />;
}
